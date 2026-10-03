package usage

import (
	"fmt"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

func TestResponseIdentityCorrelation(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	base := Record{Time: at, Provider: "p", Agent: "codex", Session: "label", NativeSession: "s", ResponseID: "resp_a", Input: 10, Output: 2, Status: 200}
	call := sessions.Call{Time: at.Add(5 * time.Second), Agent: "codex", Session: "s", ResponseID: "resp_a", Tokens: sessions.Tokens{Input: 10, Output: 2}}
	tests := []struct {
		name                   string
		edit                   func(*Record, *sessions.Call)
		nGateway, nLocal, want int
	}{
		{"response ID tolerates observation delay", nil, 1, 1, 1},
		{"gateway wins differing token observations", func(r *Record, c *sessions.Call) { c.Input = 9 }, 1, 1, 1},
		{"request identity remains independent", func(r *Record, c *sessions.Call) { r.RequestID = "req"; c.RequestID = "req" }, 1, 1, 1},
		{"request ID conflict", func(r *Record, c *sessions.Call) { r.RequestID = "req1"; c.RequestID = "req2" }, 1, 1, 0},
		{"response ID conflict", func(r *Record, c *sessions.Call) { r.RequestID = "req"; c.RequestID = "req"; c.ResponseID = "resp_b" }, 1, 1, 0},
		{"session conflict", func(r *Record, c *sessions.Call) { c.Session = "another" }, 1, 1, 0},
		{"unknown native session", func(r *Record, c *sessions.Call) { r.Session = ""; r.NativeSession = "" }, 1, 1, 1},
		{"agent conflict", func(r *Record, c *sessions.Call) { c.Agent = "opencode" }, 1, 1, 0},
		{"compatible Claude clients", func(r *Record, c *sessions.Call) { r.Agent = "claude"; c.Agent = "claude-desktop" }, 1, 1, 1},
		{"computer conflict", func(r *Record, c *sessions.Call) { r.Computer = "remote" }, 1, 1, 0},
		{"multiple gateway candidates", nil, 2, 1, 0},
		{"multiple local candidates", nil, 1, 2, 0},
		{"legacy gateway accurate timestamp", func(r *Record, c *sessions.Call) { r.ResponseID = ""; c.Time = at }, 1, 1, 1},
		{"legacy gateway keeps strict time window", func(r *Record, c *sessions.Call) { r.ResponseID = "" }, 1, 1, 0},
		{"explicit different IDs cannot time match", func(r *Record, c *sessions.Call) { c.ResponseID = "resp_b"; c.Time = at }, 1, 1, 0},
		{"real zero-token failed request", func(r *Record, c *sessions.Call) {
			r.Input = 0
			r.Output = 0
			r.Status = 500
			c.Tokens = sessions.Tokens{}
			c.Error = "failed"
		}, 1, 1, 1},
		{"rejected request remains separate", func(r *Record, c *sessions.Call) { r.Rejected = true }, 1, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, c := base, call
			if tt.edit != nil {
				tt.edit(&r, &c)
			}
			var rs []Record
			var cs []sessions.Call
			for range tt.nGateway {
				rs = append(rs, r)
			}
			for range tt.nLocal {
				cs = append(cs, c)
			}
			if got := len(gatewayMatches(rs, cs)); got != tt.want {
				t.Fatalf("matched %d want %d", got, tt.want)
			}
		})
	}
	// A response ID reused in another known session must not create ambiguity.
	other := base
	other.NativeSession = "elsewhere"
	if got := len(gatewayMatches([]Record{base, other}, []sessions.Call{call})); got != 1 {
		t.Fatalf("scoped match %d", got)
	}
	claudeRecord, claudeCall := base, call
	claudeRecord.Agent, claudeCall.Agent = "claude", "claude-desktop"
	claudeCall.ResponseID, claudeCall.Msg = "", base.ResponseID
	if len(gatewayMatches([]Record{claudeRecord}, []sessions.Call{claudeCall})) != 1 {
		t.Fatal("Claude message ID was not used as response identity")
	}

	anonymous := base
	anonymous.ResponseID = ""
	anonymous.Time = call.Time
	if got := len(gatewayMatches([]Record{base, base, anonymous}, []sessions.Call{call})); got != 0 {
		t.Fatalf("ambiguous identity used anonymous fallback: %d", got)
	}

}

// Deliberately exhaustive independent oracle for scope-aware ID matching and
// fallback uniqueness. Used by randomized tests against the indexed matcher.
func exhaustiveIdentityRows(gateway *rowChunk, chunks []*rowChunk, skip map[rowRef]bool, since time.Time) map[rowRef]bool {
	var gs, ls []rowRef
	gatewaySince := since
	if !since.IsZero() {
		gatewaySince = since.Add(-24 * time.Hour)
	}
	for i := range gateway.Rows {
		r := gateway.row(i)
		if !r.IsRejected() && !r.Time.Before(gatewaySince) {
			gs = append(gs, rowRef{gateway, i})
		}
	}
	for _, c := range chunks {
		for i, p := range c.Rows {
			ref := rowRef{c, i}
			if !skip[ref] && !p.Time.Before(since) {
				ls = append(ls, ref)
			}
		}
	}
	family := func(s string) string {
		switch s {
		case "claude", "claude-desktop", "cowork":
			return "claude"
		}
		return s
	}
	session := func(r Row) string {
		if r.NativeSession != "" {
			return r.NativeSession
		}
		return r.Session
	}
	matched, used := map[rowRef]bool{}, map[rowRef]bool{}
	pairs, counts := map[rowRef][]rowRef{}, map[rowRef]int{}
	for _, l := range ls {
		r := l.row()
		for _, g := range gs {
			v := g.row()
			a, b := session(r), session(v)
			if r.Computer != v.Computer || family(r.Agent) != family(v.Agent) || a != "" && b != "" && a != b {
				continue
			}
			if r.RequestID != "" && v.RequestID != "" && r.RequestID != v.RequestID || r.ResponseID != "" && v.ResponseID != "" && r.ResponseID != v.ResponseID {
				continue
			}
			if r.RequestID != "" && r.RequestID == v.RequestID || r.ResponseID != "" && r.ResponseID == v.ResponseID {
				pairs[l] = append(pairs[l], g)
				counts[g]++
			}
		}
	}
	for l, ps := range pairs {
		if len(ps) == 1 && counts[ps[0]] == 1 {
			matched[l], used[ps[0]] = true, true
		}
	}
	strongPairs, strongCounts := pairs, counts
	pairs, counts = map[rowRef][]rowRef{}, map[rowRef]int{}
	for _, l := range ls {
		if matched[l] || len(strongPairs[l]) > 0 {
			continue
		}
		r := l.row()
		failed := l.Chunk.Rows[l.Index].Flags&16 != 0
		if session(r) == "" || r.Input+r.Output+r.CacheRead+r.CacheWrite == 0 && !failed {
			continue
		}
		for _, g := range gs {
			if used[g] || strongCounts[g] > 0 {
				continue
			}
			v := g.row()
			if r.RequestID != "" && v.RequestID != "" || r.ResponseID != "" && v.ResponseID != "" {
				continue
			}
			if r.Computer != v.Computer || session(r) != session(v) || r.Agent != v.Agent || failed != v.Failed() || r.Input+r.CacheWrite != v.Input+v.CacheWrite || r.Output != v.Output || r.CacheRead != v.CacheRead {
				continue
			}
			end := v.Time.Add(time.Duration(v.Millis) * time.Millisecond)
			if r.Time.Before(end.Add(-2*time.Second)) || r.Time.After(end.Add(2*time.Second)) {
				continue
			}
			pairs[l] = append(pairs[l], g)
			counts[g]++
		}
	}
	for l, ps := range pairs {
		if len(ps) == 1 && counts[ps[0]] == 1 {
			matched[l] = true
		}
	}
	return matched
}

func TestVisibleLocalClaudeCopies(t *testing.T) {
	cli, desktop := &rowChunk{}, &rowChunk{}
	cli.add(Row{Record: Record{Agent: "claude", RequestID: "req"}}, "same-message", 0, false)
	// Desktop may omit the request header even when it copies the same message.
	desktop.add(Row{Record: Record{Agent: "claude-desktop"}}, "same-message", 1, false)
	duplicates := visibleLocal([]*rowChunk{cli, desktop})
	if len(duplicates) != 1 || !duplicates[rowRef{desktop, 0}] {
		t.Fatalf("CLI/Desktop copies counted separately: %+v", duplicates)
	}
}

func BenchmarkResponseIdentityAmbiguous(b *testing.B) {
	for _, n := range []int{1000, 4000, 8000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			gateway, local := &rowChunk{}, &rowChunk{}
			for i := 0; i < n; i++ {
				r := Record{Time: time.Unix(0, 0), Provider: "p", Agent: "codex", Session: "s", ResponseID: "replayed", Input: 1}
				gateway.add(Row{Record: r}, "", int64(i), false)
				local.add(Row{Record: r}, "", int64(i), false)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(matchedLocal(gateway, []*rowChunk{local}, nil, time.Time{})) != 0 {
					b.Fatal("ambiguous matches")
				}
			}
		})
	}
}
