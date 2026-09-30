package usage

// The ledger: every call of a period, one row each, newest first — what
// the agent asked for, where it went, what model answered, the tokens and
// what they cost at list price — to set beside a vendor's own bill.

import (
	"encoding/csv"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// Row is one call as the ledger lists it.
type Row struct {
	Record
	// Cost is the call's list price in USD, when its model's is known
	// (Priced); Swapped: the reply named another model than Model
	Cost    float64 `json:"cost"`
	Priced  bool    `json:"priced"`
	Swapped bool    `json:"swapped,omitempty"`
	// Source is "log" for a call read from an agent's own session file,
	// which the gateway never saw: no status, no provider, no model asked
	// for, but what the file says of the call. "" for one the gateway logged.
	Source string `json:"source,omitempty"`
}

// Failed is whether the call failed: the agent was answered an error status,
// or, for a call read from a session file, which has no status, the file
// says an error ended it.
func (r Record) Failed() bool { return r.Status >= 400 || r.Error != "" }

// Filter narrows the ledger: to one agent (its id, as AgentOf gives it), to
// one provider, to the failed calls, and to the rows whose models, provider, host or
// session hold Query (any case).
type Filter struct {
	Agent    string
	Provider string // a provider's id, as the ledger's rows have it
	Failed   bool
	Query    string
}

func (f Filter) keeps(r Record) bool {
	if f.Agent != "" && AgentOf(r.Agent) != f.Agent {
		return false
	}
	if f.Provider != "" && r.Provider != f.Provider {
		return false
	}
	if f.Failed && !r.Failed() {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" {
		return slices.ContainsFunc([]string{r.Requested, r.Model, r.Served, r.Provider, r.Host, r.Session, r.Effort, r.SessionProvider, r.SessionAccount}, func(s string) bool {
			return strings.Contains(strings.ToLower(s), q)
		})
	}
	return true
}

// Since is when the period began, as of now; zero for all.
func (p Period) Since(now time.Time) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch p {
	case Today:
		return day
	case Week:
		return day.AddDate(0, 0, -6)
	case Month:
		return day.AddDate(0, 0, -29)
	}
	return time.Time{}
}

// LogCalls reads the calls the agents' own session files record, which the
// ledger adds to those the gateway logged. A variable for the tests.
var LogCalls = sessions.Calls

// Ledgered is a period's calls that a filter keeps, newest first, with their
// sum, and the agents and providers that made any call in the period (their
// ids, for a filter to offer).
type Ledgered struct {
	Rows      []Row
	Sum       Totals
	Agents    []string
	Providers []string
}

// LedgerOf is the calls of a period that the filter keeps. The gateway's log
// comes with the calls the agents' session files record that the gateway did
// not see.
func LedgerOf(p Period, f Filter) Ledgered {
	since := p.Since(time.Now())
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	} // calls ending across the period boundary
	rows, sum, agents, providers := ledgerWith(since, f, Load(gatewaySince), LogCalls(since))
	return Ledgered{rows, sum, agents, providers}
}

// Filtered narrows one snapshot without reloading either log or repricing rows.
func (l Ledgered) Filtered(f Filter) Ledgered {
	if f == (Filter{}) {
		return l
	}
	out := Ledgered{Rows: []Row{}, Agents: l.Agents, Providers: l.Providers}
	for _, r := range l.Rows {
		if f.keeps(r.Record) {
			out.Rows = append(out.Rows, r)
			if !r.IsRejected() {
				out.Sum.addRow(r)
			}
		}
	}
	return out
}

// Ledger is LedgerOf's rows, sum and agents.
func Ledger(p Period, f Filter) (rows []Row, sum Totals, agents []string) {
	l := LedgerOf(p, f)
	return l.Rows, l.Sum, l.Agents
}

func ledger(since time.Time, f Filter, recs []Record) (rows []Row, sum Totals, agents []string) {
	rows, sum, agents, _ = ledgerWith(since, f, recs, nil)
	return rows, sum, agents
}

// UnknownProvider names calls whose session file does not identify the upstream.
// The client alone cannot distinguish a subscription from a third-party endpoint.
const UnknownProvider = "session-unknown"

// logRecord is a call read from a session file, as a record: the upstream is
// unknown, and what the file doesn't say is left out. Nothing
// stood between the agent and the vendor, so what was sent is what the agent
// asked for. Claude Code's file names the model it asked for (as it runs) and
// the one the API answered with, which is the served model; Codex's names the
// one it asked for.
func logRecord(c sessions.Call) Record {
	r := Record{Time: c.Time, Agent: c.Agent, Provider: UnknownProvider, Model: c.Model, Served: c.Model, Requested: c.Requested,
		Input: c.Input, Output: c.Output, CacheRead: c.CacheRead, CacheWrite: c.CacheWrite, Reasoning: c.Reasoning, Effort: c.Effort,
		Millis: c.Millis, TTFT: c.TTFT, Session: c.Session, RequestID: c.RequestID, Error: c.ErrorText, ErrType: c.Error}
	if c.Agent == "codex" {
		r.Requested, r.Served = c.Model, ""
	}
	if r.Requested != "" {
		// sent as asked, less the size of its context Claude Code names it by
		r.Model = contextTail.ReplaceAllString(r.Requested, "")
	}
	return r
}

// gatewayMatches correlates IDs first, then native session, equal token counts
// and an end within 2s. Fallback matches must be unique in both logs: an
// ambiguous direct call stays visible. One gateway entry consumes one file call.
func gatewayMatches(recs []Record, logs []sessions.Call) map[int]bool {
	byID, bySession := map[string][]int{}, map[string][]int{}
	for i, r := range recs {
		if r.IsRejected() {
			continue
		}
		if r.RequestID != "" {
			byID[r.RequestID] = append(byID[r.RequestID], i)
		}
		session := r.NativeSession
		if session == "" {
			session = r.Session
		}
		if session != "" {
			bySession[session] = append(bySession[session], i)
		}
	}
	matched, used := map[int]bool{}, map[int]bool{}
	for j, c := range logs {
		if c.RequestID == "" {
			continue
		}
		for _, i := range byID[c.RequestID] {
			if !used[i] {
				matched[j], used[i] = true, true
				break
			}
		}
	}
	candidates := map[int][]int{}
	counts := map[int]int{}
	for j, c := range logs {
		if matched[j] || c.Session == "" {
			continue
		}
		for _, i := range bySession[c.Session] {
			r := recs[i]
			if used[i] || c.RequestID != "" && r.RequestID != "" {
				continue
			}
			// Empty successes carry too little evidence. Failed calls may have
			// zero tokens, but both sources must agree that the call failed.
			if c.Input+c.Output+c.CacheRead+c.CacheWrite == 0 && (c.Error == "" || !r.Failed()) {
				continue
			}
			if (c.Error != "") != r.Failed() {
				continue
			}

			if AgentOf(r.Agent) != c.Agent || r.Input != c.Input || r.Output != c.Output || r.CacheRead != c.CacheRead || r.CacheWrite != c.CacheWrite {
				continue
			}
			end := r.Time.Add(time.Duration(r.Millis) * time.Millisecond)
			if c.Time.Before(end.Add(-2*time.Second)) || c.Time.After(end.Add(2*time.Second)) {
				continue
			}
			candidates[j] = append(candidates[j], i)
			counts[i]++
		}
	}
	for j, cs := range candidates {
		if len(cs) == 1 && counts[cs[0]] == 1 {
			matched[j] = true
		}
	}
	return matched
}

func ledgerWith(since time.Time, f Filter, recs []Record, logs []sessions.Call) (rows []Row, sum Totals, agents, providers []string) {
	renamed := provider.Renamed()
	priceOf := pricer()
	rows = make([]Row, 0, len(recs)+len(logs))
	agents, providers = []string{}, []string{}
	matched := gatewayMatches(recs, logs)
	identities := newSessionResolver(logs)
	add := func(r Record, pr *catalog.Price, source string) {
		if a := AgentOf(r.Agent); !slices.Contains(agents, a) {
			agents = append(agents, a)
		}
		if r.Provider != "" && !slices.Contains(providers, r.Provider) {
			providers = append(providers, r.Provider)
		}
		if !f.keeps(r) {
			return
		}
		if !r.IsRejected() {
			sum.add(r, pr)
		}
		row := Row{Record: r, Swapped: r.Served != "" && Swapped(r.Model, r.Served), Source: source}
		if pr != nil && r.Input+r.Output > 0 {
			row.Cost, row.Priced = pr.Cost(r.Input, r.Output, r.CacheRead, r.CacheWrite), true
		}
		row.Agent = AgentOf(r.Agent)
		rows = append(rows, row)
	}
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if !since.IsZero() && r.Time.Before(since) {
			continue
		}
		if id, ok := renamed[r.Provider]; ok {
			r.Provider = id
		}
		add(r, priceOf(r), "")
	}
	for i, c := range logs {
		if (!since.IsZero() && c.Time.Before(since)) || matched[i] {
			continue
		}
		// Gateway and session calls use the same model API list price,
		// independently of route and account attribution.
		r := logRecord(c)
		id := identities.resolve(c)
		r.Provider, r.Host, r.SessionAccount = id.provider, id.user, id.account
		r.SessionProvider = c.Upstream
		add(r, priceOf(r), "log")
	}
	// the two logs, by when each call began
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Time.After(rows[j].Time) })
	slices.Sort(agents)
	slices.Sort(providers)
	return rows, sum, agents, providers
}

// pricer uses one API list price per model, irrespective of subscriptions,
// relays, account identity or an unknown route. Vendor discounts are not used.
func pricer() func(Record) *catalog.Price {
	prices := map[string]*catalog.Price{}
	return func(r Record) *catalog.Price {
		k := r.Model
		if pr, ok := prices[k]; ok {
			return pr
		}
		var pr *catalog.Price
		v, ok := provider.MakerPrice(r.Model)
		if ok {
			pr = &v
		} else {
			catalog.Missing() // models.dev may list it by now
		}
		prices[k] = pr
		return pr
	}
}

// CSVHeader is the ledger's columns, as WriteCSV writes them.
var CSVHeader = []string{"time", "agent", "requested_model", "provider", "host", "model", "served_model", "swapped",
	"effort", "input_tokens", "output_tokens", "cache_write_tokens", "cache_read_tokens", "reasoning_tokens",
	"cost_usd", "duration_ms", "ttft_ms", "status", "error", "session", "kind",
	"request_id", "endpoint", "error_message", "error_type", "source", "rejected", "session_provider", "session_account"}

// WriteCSV writes rows as CSV, a header first: times in RFC 3339 with
// their offset, the cost in USD at list price (empty when unknown), error
// "true" for a call that failed: answered with a status of 400 or more, or
// ended by an error a session file tells.
func WriteCSV(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	cw.Write(CSVHeader)
	n := strconv.Itoa
	for _, r := range rows {
		cost := ""
		if r.Priced {
			cost = strconv.FormatFloat(r.Cost, 'f', 6, 64)
		}
		ttft := ""
		if r.TTFT > 0 {
			ttft = strconv.FormatInt(r.TTFT, 10)
		}
		cw.Write([]string{r.Time.Format(time.RFC3339), r.Agent, r.Requested, r.Provider, r.Host, r.Model, r.Served,
			strconv.FormatBool(r.Swapped), r.Effort, n(r.Input), n(r.Output), n(r.CacheWrite), n(r.CacheRead), n(r.Reasoning),
			cost, strconv.FormatInt(r.Millis, 10), ttft, n(r.Status), strconv.FormatBool(r.Failed()), r.Session, r.Kind,
			r.RequestID, r.Endpoint, r.Error, r.ErrType, r.Source, strconv.FormatBool(r.IsRejected()), r.SessionProvider, r.SessionAccount})
	}
	cw.Flush()
	return cw.Error()
}

// timeline is a period's empty timeline as of now, so quiet stretches still
// take their place: a point per hour of today, per day of a week or a month,
// and per week (from a Monday) of a longer time, which starts at first, the
// time of the oldest call. since is when it starts.
func timeline(p Period, now, first time.Time) (since time.Time, bucket string, pts []Point) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	bucket, pts = "day", []Point{}
	var n int
	switch p {
	case Today:
		since, bucket = day, "hour"
	case Week:
		since, n = day.AddDate(0, 0, -6), 7
	case Month:
		since, n = day.AddDate(0, 0, -29), 30
	default:
		if !first.IsZero() {
			f := first.In(now.Location())
			since = time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, now.Location())
		} else {
			since = day
		}
		if n = calendarDays(since, day) + 1; n > 60 {
			bucket = "week"
			// start on the Monday of the first week
			off := (int(since.Weekday()) + 6) % 7
			since = since.AddDate(0, 0, -off)
			n = calendarDays(since, day)/7 + 1
		}
	}
	switch bucket {
	case "hour":
		for h := 0; day.Add(time.Duration(h) * time.Hour).Before(day.AddDate(0, 0, 1)); h++ {
			t := day.Add(time.Duration(h) * time.Hour)
			pts = append(pts, Point{Label: t.Format("15"), Time: t})
		}
	case "day":
		for i := 0; i < n; i++ {
			t := since.AddDate(0, 0, i)
			pts = append(pts, Point{Label: t.Format("Jan 2"), Time: t})
		}
	case "week":
		for i := 0; i < n; i++ {
			t := since.AddDate(0, 0, 7*i)
			pts = append(pts, Point{Label: t.Format("Jan 2"), Time: t})
		}
	}
	return since, bucket, pts
}

func calendarDays(from, to time.Time) int {
	date := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
	return int(date(to).Sub(date(from)).Hours() / 24)
}

func bucketIndex(bucket string, since, t time.Time) int {
	switch bucket {
	case "hour":
		return int(t.Sub(since).Hours())
	case "week":
		return calendarDays(since, t) / 7
	default:
		return calendarDays(since, t)
	}
}

// addRow adds a ledger row, at the cost it already has.
func (t *Totals) addRow(r Row) {
	if r.TTFT > 0 && !r.Failed() {
		t.Timed++
		t.TTFT += r.TTFT
		if r.Output > 0 && r.Millis > r.TTFT {
			t.DecodeMs += r.Millis - r.TTFT
			t.DecodeOut += r.Output
		}
	}
	t.Calls++
	if r.Failed() {
		t.Errors++
	}
	t.Input += r.Input
	t.Output += r.Output
	t.CacheRead += r.CacheRead
	t.CacheWrite += r.CacheWrite
	t.Reasoning += r.Reasoning
	switch {
	case r.Priced:
		t.Cost += r.Cost
	case r.Input+r.Output > 0:
		t.Unpriced++
	}
}

// Dimensions are what calls are told apart by: the provider they went to,
// the agent that made them, the model.
var Dimensions = []string{"provider", "agent", "model"}

// key is the row's part of a dimension: its provider's id, its agent, its model.
func (r Row) key(by string) string {
	switch by {
	case "provider":
		return r.Provider
	case "agent":
		return r.Agent
	}
	return r.Model
}

// AllTokens is what went in and out and through the cache.
func (t Totals) AllTokens() int { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

// Share is one provider's, agent's or model's part of some rows.
type Share struct {
	ID string `json:"id"`
	Totals
}

// Breakdown sums rows by a dimension, the one with the most tokens first (then
// by calls, then by name).
func Breakdown(rows []Row, by string) []Share {
	at := map[string]*Share{}
	var out []*Share
	for _, r := range rows {
		if r.IsRejected() {
			continue
		}
		k := r.key(by)
		s := at[k]
		if s == nil {
			s = &Share{ID: k}
			at[k], out = s, append(out, s)
		}
		s.addRow(r)
	}
	slices.SortFunc(out, func(a, b *Share) int {
		switch {
		case a.AllTokens() != b.AllTokens():
			return b.AllTokens() - a.AllTokens()
		case a.Calls != b.Calls:
			return b.Calls - a.Calls
		}
		return strings.Compare(a.ID, b.ID)
	})
	res := make([]Share, len(out))
	for i, s := range out {
		res[i] = *s
	}
	return res
}

// Part is what one provider, agent or model had of a point of the timeline.
type Part struct {
	Calls  int     `json:"calls"`
	Tokens int     `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// SeriesPoint is a point of the timeline with its calls told apart, by each
// dimension, for a chart to stack them: the models of one point, say.
type SeriesPoint struct {
	Point
	By map[string]map[string]Part `json:"by"`
}

// seriesKeep is how many providers, agents or models a point tells apart,
// those with the most tokens over the period; the rest are what is left of
// the point's own sums.
const seriesKeep = 24

// LedgerSeries is the rows of a period, the ledger's or a filter's, summed by
// hour, day or week, for the chart over them. A row is of the point its
// time falls in; the rows are in any order.
func LedgerSeries(p Period, rows []Row) (bucket string, pts []SeriesPoint) {
	now := time.Now()
	var first time.Time
	for _, r := range rows {
		if r.IsRejected() {
			continue
		}
		if first.IsZero() || r.Time.Before(first) {
			first = r.Time
		}
	}
	since, bucket, base := timeline(p, now, first)
	pts = make([]SeriesPoint, len(base))
	for i := range base {
		pts[i] = SeriesPoint{Point: base[i], By: map[string]map[string]Part{}}
		for _, d := range Dimensions {
			pts[i].By[d] = map[string]Part{}
		}
	}
	kept := map[string]map[string]bool{}
	for _, d := range Dimensions {
		kept[d] = map[string]bool{}
		for i, s := range Breakdown(rows, d) {
			if i < seriesKeep {
				kept[d][s.ID] = true
			}
		}
	}
	for _, r := range rows {
		if r.IsRejected() {
			continue
		}
		t := r.Time.In(now.Location())
		if t.Before(since) {
			continue
		}
		i := bucketIndex(bucket, since, t)
		if i < 0 || i >= len(pts) {
			continue
		}
		pts[i].addRow(r)
		tokens := r.Input + r.Output + r.CacheRead + r.CacheWrite
		for _, d := range Dimensions {
			if k := r.key(d); kept[d][k] {
				part := pts[i].By[d][k]
				part.Calls++
				part.Tokens += tokens
				if r.Priced {
					part.Cost += r.Cost
				}
				pts[i].By[d][k] = part
			}
		}
	}
	return bucket, pts
}
