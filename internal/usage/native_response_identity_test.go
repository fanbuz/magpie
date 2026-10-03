package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// The local events are an anonymized native A/B/A excerpt. Gateway records are
// simulated observations of those messages, with no request header and a five
// second observation delay. They are not claimed to be captured gateway logs.
func TestNativeMessagesCorrelateThroughPageAndLedger(t *testing.T) {
	pageHome(t)
	data, err := os.ReadFile("testdata/claude-native-response-ids.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions.ClaudeDir(), "projects", "fixture", "session_3.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	// Decode the additive field so this test also runs against main, which
	// ignores response_id and reports four rows with 784,330 tokens.
	var records []Record
	if err := json.Unmarshal([]byte(`[
		{"t":"2025-12-31T23:59:55Z","agent":"claude","provider":"fixture","session":"session_3","response_id":"message_4","in":5183,"out":17349,"cache_read":140533,"cache_write":32514,"status":200},
		{"t":"2026-01-01T00:23:04.343Z","agent":"claude","provider":"fixture","session":"session_3","response_id":"message_7","in":5155,"out":20014,"cache_read":140533,"cache_write":30884,"status":200}
	]`), &records); err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		Append(r)
	}
	for _, cold := range []bool{false, true} {
		if cold {
			sessions.Reset()
		}
		page := QueryPage(All, Filter{}, 0, 100)
		rows, sum, _ := Ledger(All, Filter{})
		if page.Total != 2 || len(rows) != 2 || page.Sum.Calls != 2 || sum.Calls != 2 {
			t.Fatalf("cold=%v page=%d ledger=%d", cold, page.Total, len(rows))
		}
		for _, total := range []Totals{page.Sum, sum} {
			if total.Input != 10338 || total.Output != 37363 || total.CacheRead != 281066 || total.CacheWrite != 63398 {
				t.Fatalf("cold=%v totals=%+v", cold, total)
			}
		}
	}
	if len(sessions.Calls(time.Time{})) != 2 {
		t.Fatal("native message replay was not deduplicated")
	}
}
