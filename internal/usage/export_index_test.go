package usage

import (
	"github.com/yetone/magpie/internal/sessions"
	"math/rand"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestIndexedExportMatchesExhaustiveOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 100; trial++ {
		var recs []Record
		var logs []sessions.Call
		start := time.Now()
		for i := 0; i < 80; i++ {
			r := Record{Time: start.Add(time.Duration(rng.Intn(15)) * time.Second), Agent: "codex", Session: []string{"s", "t", ""}[rng.Intn(3)], Model: "m", Input: rng.Intn(4), Status: 200, Millis: int64(rng.Intn(4) * 1000)}
			if rng.Intn(3) == 0 {
				r.RequestID = []string{"a", "b", "c"}[rng.Intn(3)]
			}
			if rng.Intn(5) == 0 {
				r.Status = 500
				r.Error = "failed"
			}
			if rng.Intn(7) == 0 {
				r.Rejected = true
			}
			if rng.Intn(2) == 0 {
				r.NativeSession = r.Session
				r.Session = "overridden"
			}
			recs = append(recs, r)
			c := sessions.Call{Time: start.Add(time.Duration(rng.Intn(15)) * time.Second), Agent: "codex", Session: []string{"s", "t", ""}[rng.Intn(3)], Tokens: sessions.Tokens{Input: rng.Intn(4)}}
			if rng.Intn(3) == 0 {
				c.RequestID = []string{"a", "b", "c"}[rng.Intn(3)]
			}
			if rng.Intn(5) == 0 {
				c.Error = "failed"
			}
			logs = append(logs, c)
		}
		got, want := gatewayMatches(recs, logs), exhaustiveGatewayMatches(recs, logs)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("trial %d: got %v want %v", trial, got, want)
		}
	}
}

func BenchmarkExportAmbiguousSession(b *testing.B) {
	for _, n := range []int{1000, 4000, 8000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			recs := make([]Record, n)
			logs := make([]sessions.Call, n)
			now := time.Now()
			for i := range recs {
				recs[i] = Record{Time: now, Agent: "codex", Session: "s", Input: 1, Status: 200}
				logs[i] = sessions.Call{Time: now, Agent: "codex", Session: "s", Tokens: sessions.Tokens{Input: 1}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				gatewayMatches(recs, logs)
			}
		})
	}
}
func exhaustiveGatewayMatches(recs []Record, logs []sessions.Call) map[int]bool {
	gateway, local := &rowChunk{}, &rowChunk{}
	for i, r := range recs {
		r.Agent = AgentOf(r.Agent)
		gateway.add(Row{Record: r}, "", int64(i), false)
	}
	for i, c := range logs {
		local.add(Row{Record: logRecord(c)}, "", int64(i), c.Error != "")
	}
	out := map[int]bool{}
	for ref := range exhaustiveIdentityRows(gateway, []*rowChunk{local}, nil, time.Time{}) {
		out[ref.Index] = true
	}
	return out
}
