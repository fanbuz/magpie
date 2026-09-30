package usage

// The request page keeps compact per-source records, not a second full ledger.
// Unchanged sources are never decoded again. Aggregation streams those records;
// only the requested page is materialized/sorted. Export can still ask for the
// complete ledger explicitly. This cache is disposable and bounded.

import (
	"bufio"
	"container/heap"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

type RequestPage struct {
	Rows              []Row
	Sum               Totals
	Total             int
	Agents, Providers []string
	Bucket            string
	Series            []SeriesPoint
	By                map[string][]Share
}

type packedRow struct {
	Time                           time.Time
	Text                           [18]uint32
	Tokens                         [5]int64
	Millis, TTFT, FirstText, Order int64
	Cost                           float64
	Status                         int32
	Flags                          uint8
}
type rowChunk struct {
	Rows    []packedRow
	Strings []string
	dict    map[string]uint32
	Source  sessions.CallSource
	Bytes   int64
	Used    uint64
}

func rowText(r *Row) [17]*string {
	return [17]*string{&r.Agent, &r.Provider, &r.Host, &r.SessionProvider, &r.SessionAccount, &r.Model, &r.Requested, &r.Served, &r.Effort, &r.Error, &r.ErrType, &r.RequestID, &r.Endpoint, &r.Session, &r.NativeSession, &r.Kind, &r.Source}
}
func (c *rowChunk) add(r Row, msg string, order int64, failed bool) {
	if c.dict == nil {
		c.dict = map[string]uint32{"": 0}
		c.Strings = []string{""}
	}
	intern := func(s string) uint32 {
		if id, ok := c.dict[s]; ok {
			return id
		}
		id := uint32(len(c.Strings))
		c.dict[s] = id
		c.Strings = append(c.Strings, s)
		c.Bytes += int64(len(s) + 48)
		return id
	}
	p := packedRow{Time: r.Time, Tokens: [5]int64{int64(r.Input), int64(r.Output), int64(r.CacheRead), int64(r.CacheWrite), int64(r.Reasoning)}, Millis: r.Millis, TTFT: r.TTFT, FirstText: r.FirstText, Order: order, Cost: r.Cost, Status: int32(r.Status)}
	for i, s := range rowText(&r) {
		p.Text[i] = intern(*s)
	}
	p.Text[17] = intern(msg)
	if r.Priced {
		p.Flags |= 1
	}
	if r.Swapped {
		p.Flags |= 2
	}
	if r.Rejected {
		p.Flags |= 4
	}
	if r.SessionOfficialLogin {
		p.Flags |= 8
	}
	if failed {
		p.Flags |= 16
	}
	c.Rows = append(c.Rows, p)
	c.Bytes += 192
}
func (c *rowChunk) row(i int) Row {
	p := &c.Rows[i]
	r := Row{Record: Record{Time: p.Time, Input: int(p.Tokens[0]), Output: int(p.Tokens[1]), CacheRead: int(p.Tokens[2]), CacheWrite: int(p.Tokens[3]), Reasoning: int(p.Tokens[4]), Millis: p.Millis, TTFT: p.TTFT, FirstText: p.FirstText, Status: int(p.Status), Rejected: p.Flags&4 != 0, SessionOfficialLogin: p.Flags&8 != 0}, Cost: p.Cost, Priced: p.Flags&1 != 0, Swapped: p.Flags&2 != 0}
	for i, s := range rowText(&r) {
		*s = c.Strings[p.Text[i]]
	}
	return r
}

type rowRef struct {
	Chunk *rowChunk
	Index int
}

func (r rowRef) row() Row      { return r.Chunk.row(r.Index) }
func (r rowRef) at() time.Time { return r.Chunk.Rows[r.Index].Time }
func refNewer(a, b rowRef) bool {
	if !a.at().Equal(b.at()) {
		return a.at().After(b.at())
	}
	if (a.Chunk.Source.Path == "") != (b.Chunk.Source.Path == "") {
		return a.Chunk.Source.Path == ""
	}
	if a.Chunk.Source.Path != b.Chunk.Source.Path {
		return a.Chunk.Source.Path > b.Chunk.Source.Path
	}
	return a.Chunk.Rows[a.Index].Order > b.Chunk.Rows[b.Index].Order
}

type newestHeap []rowRef

func (h newestHeap) Len() int           { return len(h) }
func (h newestHeap) Less(i, j int) bool { return refNewer(h[j], h[i]) }
func (h newestHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *newestHeap) Push(v any)        { *h = append(*h, v.(rowRef)) }
func (h *newestHeap) Pop() any          { a := *h; v := a[len(a)-1]; *h = a[:len(a)-1]; return v }

type pageKey struct {
	Period        Period
	Filter        Filter
	Offset, Limit int
}
type requestIndex struct {
	sync.Mutex
	root, meta, key                     string
	chunks                              map[string]*rowChunk
	gateway                             *rowChunk
	gatewaySize, gatewayMod, gatewayOff int64
	gatewayHash                         string
	pages                               map[pageKey]RequestPage
	tick                                uint64
}

var requestCache requestIndex

const requestCacheBytes = 96 << 20
const gatewayCacheBytes = 32 << 20

func statKey(path string) string {
	s, e := os.Stat(path)
	if e != nil {
		return path + ":missing"
	}
	return fmt.Sprintf("%s:%d:%d", path, s.Size(), s.ModTime().UnixNano())
}
func recordHash(path string, n int64) string {
	f, e := os.Open(path)
	if e != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.CopyN(h, f, n); e != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func QueryPage(p Period, f Filter, offset, limit int) RequestPage {
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	offset = max(0, offset)
	// A replacement reader remains useful to callers supplying synthetic logs.
	if LogCalls != nil {
		return pageFromLedger(p, f, offset, limit, LedgerOf(p, Filter{}))
	}
	sources := sessions.CallSources()
	slices.SortFunc(sources, func(a, b sessions.CallSource) int {
		if n := a.Modified.Compare(b.Modified); n != 0 {
			return n
		}
		return strings.Compare(a.Path, b.Path)
	})
	ids, _ := json.Marshal(provider.SessionIdentities(sessions.CodexDir()))
	renamed, _ := json.Marshal(provider.Renamed())
	meta := fmt.Sprintf("%s|%s|%s|%s", ids, renamed, statKey(provider.Path()), statKey(catalog.CachePath()))
	h := sha256.New()
	for _, root := range sessions.DesktopDataDirs() {
		for _, kind := range []string{"local-agent-mode-sessions", "claude-code-sessions"} {
			files, _ := sessions.SessionGlob(filepath.Join(root, kind, "*", "*", "local_*.json"))
			for _, path := range files {
				fmt.Fprint(h, statKey(path))
			}
		}
	}
	meta += fmt.Sprintf("|%x", h.Sum(nil))
	h.Reset()
	fmt.Fprint(h, meta, statKey(Path()), time.Now().Format("2006-01-02 MST"))
	for _, s := range sources {
		fmt.Fprintf(h, "%s:%d:%d;", s.Path, s.Size, s.Modified.UnixNano())
	}
	key := fmt.Sprintf("%x", h.Sum(nil))
	root := Path() + "|" + catalog.CachePath()
	idx := &requestCache
	idx.Lock()
	defer idx.Unlock()
	if idx.root != root || idx.meta != meta {
		idx.root, idx.meta = root, meta
		idx.chunks = map[string]*rowChunk{}
		idx.gateway = nil
		idx.pages = nil
		idx.key = ""
	}
	if idx.key != key {
		idx.pages = map[pageKey]RequestPage{}
		idx.key = key
	}
	q := pageKey{p, f, offset, limit}
	if page, ok := idx.pages[q]; ok {
		return page
	}
	idx.tick++
	price := pricer()
	priceRow := func(r Record, source string) Row {
		row := Row{Record: r, Source: source, Swapped: r.Served != "" && Swapped(r.Model, r.Served)}
		if pr := price(r); pr != nil && r.Input+r.Output > 0 {
			row.Priced = true
			row.Cost = pr.Cost(r.Input, r.Output, r.CacheRead, r.CacheWrite)
		}
		row.Agent = AgentOf(r.Agent)
		return row
	}
	idx.readGateway(priceRow)
	since := p.Since(time.Now())
	on := map[string]bool{}
	var chunks []*rowChunk
	var resolver *sessionResolver
	for _, s := range sources {
		on[s.Path] = true
		if !since.IsZero() && s.Modified.Before(since) {
			continue
		}
		c := idx.chunks[s.Path]
		if c == nil || c.Source.Size != s.Size || !c.Source.Modified.Equal(s.Modified) {
			if resolver == nil {
				resolver = newSessionResolver([]sessions.Call{{}})
			}
			cs := sessions.ReadCallSource(s)
			c = &rowChunk{Source: s, Rows: make([]packedRow, 0, len(cs))}
			for _, call := range cs {
				r := logRecord(call)
				r.SessionAccount = resolver.resolve(call)
				r.SessionOfficialLogin = resolver.officialLogin(call, r.SessionAccount)
				r.SessionProvider = call.Upstream
				c.add(priceRow(r, "log"), call.Msg, call.To, call.Error != "")
			}
			c.Bytes -= int64(32 * len(c.Strings))
			c.dict = nil // local chunks are immutable until the source changes
			idx.chunks[s.Path] = c
		}
		c.Used = idx.tick
		chunks = append(chunks, c)
	}
	for path := range idx.chunks {
		if !on[path] {
			delete(idx.chunks, path)
		}
	}
	page := buildRequestPage(p, f, offset, limit, idx.gateway, chunks)
	if len(idx.pages) >= 16 {
		idx.pages = map[pageKey]RequestPage{}
	}
	idx.pages[q] = page
	var bytes int64
	var kept []*rowChunk
	for _, c := range idx.chunks {
		bytes += c.Bytes
		kept = append(kept, c)
	}
	slices.SortFunc(kept, func(a, b *rowChunk) int {
		if a.Used < b.Used {
			return -1
		}
		if a.Used > b.Used {
			return 1
		}
		return strings.Compare(a.Source.Path, b.Source.Path)
	})
	for _, c := range kept {
		if bytes <= requestCacheBytes {
			break
		}
		delete(idx.chunks, c.Source.Path)
		bytes -= c.Bytes
	}
	if idx.gateway.Bytes > gatewayCacheBytes {
		idx.gateway = nil
	}
	return page
}

func (idx *requestIndex) readGateway(price func(Record, string) Row) {
	info, err := os.Stat(Path())
	if err != nil {
		idx.gateway = &rowChunk{}
		idx.gatewaySize, idx.gatewayMod, idx.gatewayOff = 0, 0, 0
		idx.gatewayHash = ""
		return
	}
	if idx.gateway != nil && idx.gatewaySize == info.Size() && idx.gatewayMod == info.ModTime().UnixNano() {
		return
	}
	continued := idx.gateway != nil && info.Size() > idx.gatewaySize && idx.gatewayHash != "" && recordHash(Path(), idx.gatewaySize) == idx.gatewayHash
	if !continued {
		idx.gateway = &rowChunk{}
		idx.gatewayOff = 0
	}
	file, err := os.Open(Path())
	if err != nil {
		return
	}
	defer file.Close()
	if _, err = file.Seek(idx.gatewayOff, io.SeekStart); err != nil {
		return
	}
	rd := bufio.NewReader(io.LimitReader(file, info.Size()-idx.gatewayOff))
	renamed := provider.Renamed()
	for {
		b, e := rd.ReadBytes('\n')
		if e != nil {
			break
		}
		idx.gatewayOff += int64(len(b))
		var r Record
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		if id, ok := renamed[r.Provider]; ok {
			r.Provider = id
		}
		idx.gateway.add(price(r, ""), "", int64(len(idx.gateway.Rows)), false)
	}
	idx.gatewaySize, idx.gatewayMod = info.Size(), info.ModTime().UnixNano()
	idx.gatewayHash = recordHash(Path(), info.Size())
}

// localRefs visits first occurrences of Claude message IDs. It stores only
// references for matches, never a full hydrated row for every call.
func visibleLocal(chunks []*rowChunk) map[rowRef]bool {
	duplicates := map[rowRef]bool{}
	seen := map[string]bool{}
	for _, c := range chunks {
		for i, p := range c.Rows {
			msg := c.Strings[p.Text[17]]
			if msg == "" {
				continue
			}
			if seen[msg] {
				duplicates[rowRef{c, i}] = true
			} else {
				seen[msg] = true
			}
		}
	}
	return duplicates
}
func matchedLocal(gateway *rowChunk, chunks []*rowChunk, skip map[rowRef]bool, since time.Time) map[rowRef]bool {
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	}
	byID, bySession := map[string][]int{}, map[string][]int{}
	for i := range gateway.Rows {
		r := gateway.row(i)
		if r.IsRejected() || r.Time.Before(gatewaySince) {
			continue
		}
		if r.RequestID != "" {
			byID[r.RequestID] = append(byID[r.RequestID], i)
		}
		s := r.NativeSession
		if s == "" {
			s = r.Session
		}
		if s != "" {
			bySession[s] = append(bySession[s], i)
		}
	}
	byRequest := map[string][]rowRef{}
	for _, c := range chunks {
		for i, p := range c.Rows {
			ref := rowRef{c, i}
			if skip[ref] || p.Time.Before(since) {
				continue
			}
			id := c.Strings[p.Text[11]]
			if len(byID[id]) > 0 {
				byRequest[id] = append(byRequest[id], ref)
			}
		}
	}
	matched, used := map[rowRef]bool{}, map[int]bool{}
	for id, refs := range byRequest {
		slices.SortFunc(refs, func(a, b rowRef) int {
			if refNewer(a, b) {
				return -1
			}
			if refNewer(b, a) {
				return 1
			}
			return 0
		})
		for j, ref := range refs {
			if j >= len(byID[id]) {
				break
			}
			matched[ref] = true
			used[byID[id][j]] = true
		}
	}
	candidates := map[rowRef][]int{}
	counts := map[int]int{}
	for _, c := range chunks {
		for j, p := range c.Rows {
			ref := rowRef{c, j}
			if skip[ref] || matched[ref] || p.Time.Before(since) {
				continue
			}
			s := c.Strings[p.Text[13]]
			if len(bySession[s]) == 0 {
				continue
			}
			r := c.row(j)
			failed := p.Flags&16 != 0
			for _, i := range bySession[s] {
				g := gateway.row(i)
				if used[i] || r.RequestID != "" && g.RequestID != "" {
					continue
				}
				if r.Input+r.Output+r.CacheRead+r.CacheWrite == 0 && (!failed || !g.Failed()) {
					continue
				}
				if failed != g.Failed() || g.Agent != r.Agent || g.Input != r.Input || g.Output != r.Output || g.CacheRead != r.CacheRead || g.CacheWrite != r.CacheWrite {
					continue
				}
				end := g.Time.Add(time.Duration(g.Millis) * time.Millisecond)
				if r.Time.Before(end.Add(-2*time.Second)) || r.Time.After(end.Add(2*time.Second)) {
					continue
				}
				candidates[ref] = append(candidates[ref], i)
				counts[i]++
			}
		}
	}
	for ref, cs := range candidates {
		if len(cs) == 1 && counts[cs[0]] == 1 {
			matched[ref] = true
		}
	}
	return matched
}
func sharesOf(m map[string]*Share) []Share {
	out := make([]Share, 0, len(m))
	for _, s := range m {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b Share) int {
		if a.AllTokens() != b.AllTokens() {
			return b.AllTokens() - a.AllTokens()
		}
		if a.Calls != b.Calls {
			return b.Calls - a.Calls
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}
func buildRequestPage(p Period, f Filter, offset, limit int, gateway *rowChunk, chunks []*rowChunk) RequestPage {
	skip := visibleLocal(chunks)
	since := p.Since(time.Now())
	matched := matchedLocal(gateway, chunks, skip, since)
	all := append([]*rowChunk{gateway}, chunks...)
	visit := func(fn func(rowRef, Row)) {
		for _, c := range all {
			for i, pr := range c.Rows {
				ref := rowRef{c, i}
				if pr.Time.Before(since) || skip[ref] || matched[ref] {
					continue
				}
				fn(ref, c.row(i))
			}
		}
	}
	out := RequestPage{Rows: []Row{}, Agents: []string{}, Providers: []string{}, By: map[string][]Share{}}
	agents, providers := map[string]bool{}, map[string]bool{}
	groups := map[string]map[string]*Share{}
	seriesGroups := map[string]map[string]*Share{}
	for _, d := range Dimensions {
		groups[d] = map[string]*Share{}
		seriesGroups[d] = map[string]*Share{}
	}
	// Bound the requested prefix before adding limit: external offsets can
	// reach MaxInt, and a page beyond all sources needs no heap at all.
	maxRows := 0
	for _, c := range all {
		maxRows += len(c.Rows)
	}
	take := 0
	if offset < maxRows {
		take = offset + min(limit, maxRows-offset)
	}
	selected := newestHeap{}
	var first time.Time
	visit(func(ref rowRef, r Row) {
		agents[r.Agent] = true
		if r.Provider != "" {
			providers[r.Provider] = true
		}
		keep := f.keeps(r.Record)
		if keep {
			out.Total++
			if take > 0 && len(selected) < take {
				heap.Push(&selected, ref)
			} else if take > 0 && refNewer(ref, selected[0]) {
				selected[0] = ref
				heap.Fix(&selected, 0)
			}
			if !r.IsRejected() {
				out.Sum.addRow(r)
				for _, d := range Dimensions {
					k := r.key(d)
					s := seriesGroups[d][k]
					if s == nil {
						s = &Share{ID: k}
						seriesGroups[d][k] = s
					}
					s.addRow(r)
				}
				if first.IsZero() || r.Time.Before(first) {
					first = r.Time
				}
			}
		}
		if r.IsRejected() {
			return
		}
		for _, d := range Dimensions {
			g := f
			if d == "provider" {
				g.Provider = ""
			}
			if d == "agent" {
				g.Agent = ""
			}
			if !g.keeps(r.Record) {
				continue
			}
			k := r.key(d)
			s := groups[d][k]
			if s == nil {
				s = &Share{ID: k}
				groups[d][k] = s
			}
			s.addRow(r)
		}
	})
	slices.SortFunc(selected, func(a, b rowRef) int {
		if refNewer(a, b) {
			return -1
		}
		if refNewer(b, a) {
			return 1
		}
		return 0
	})
	for _, ref := range selected[min(offset, len(selected)):] {
		out.Rows = append(out.Rows, ref.row())
	}
	for a := range agents {
		out.Agents = append(out.Agents, a)
	}
	slices.Sort(out.Agents)
	for a := range providers {
		out.Providers = append(out.Providers, a)
	}
	slices.Sort(out.Providers)
	for _, d := range Dimensions {
		out.By[d] = sharesOf(groups[d])
	}
	var base []Point
	chartSince := since
	chartSince, out.Bucket, base = timeline(p, time.Now(), first)
	out.Series = make([]SeriesPoint, len(base))
	for i := range base {
		out.Series[i] = SeriesPoint{Point: base[i], By: map[string]map[string]Part{}}
		for _, d := range Dimensions {
			out.Series[i].By[d] = map[string]Part{}
		}
	}
	visit(func(_ rowRef, r Row) {
		if !f.keeps(r.Record) || r.IsRejected() {
			return
		}
		t := r.Time.In(time.Local)
		if t.Before(chartSince) {
			return
		}
		i := bucketIndex(out.Bucket, chartSince, t)
		if i < 0 || i >= len(out.Series) {
			return
		}
		pt := &out.Series[i]
		pt.addRow(r)
		for _, d := range Dimensions {
			k := r.key(d)
			part := pt.By[d][k]
			part.Calls++
			part.Tokens += r.Input + r.Output + r.CacheRead + r.CacheWrite
			if r.Priced {
				part.Cost += r.Cost
			}
			pt.By[d][k] = part
		}
	})
	// Match LedgerSeries's top-24 selection on the filtered data, not facets.
	for _, d := range Dimensions {
		kept := map[string]bool{}
		for i, s := range sharesOf(seriesGroups[d]) {
			if i < seriesKeep {
				kept[s.ID] = true
			}
		}
		for i := range out.Series {
			for k := range out.Series[i].By[d] {
				if !kept[k] {
					delete(out.Series[i].By[d], k)
				}
			}
		}
	}
	return out
}
func pageFromLedger(p Period, f Filter, offset, limit int, all Ledgered) RequestPage {
	l := all.Filtered(f)
	out := RequestPage{Sum: l.Sum, Total: len(l.Rows), Agents: l.Agents, Providers: l.Providers, By: map[string][]Share{}}
	offset = min(offset, len(l.Rows))
	out.Rows = l.Rows[offset:min(len(l.Rows), offset+limit)]
	out.Bucket, out.Series = LedgerSeries(p, l.Rows)
	for _, d := range Dimensions {
		g := f
		if d == "provider" {
			g.Provider = ""
		}
		if d == "agent" {
			g.Agent = ""
		}
		out.By[d] = Breakdown(all.Filtered(g).Rows, d)
	}
	return out
}
