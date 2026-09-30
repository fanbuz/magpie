package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// claudeCedarEmber is claude.ai's usage answer as a Max subscriber shared
// it (Discord), asked with cedar_ember=1: one usage-limit reset, for Opus
// 5.5's launch.
const claudeCedarEmber = `{
  "five_hour": {"utilization": 84, "resets_at": "2026-09-30T12:00:00+00:00"},
  "seven_day": {"utilization": 92, "resets_at": "2026-10-02T07:00:00+00:00"},
  "cedar_ember": {
    "eligible": true, "ineligible_reason": null, "at_limit": false, "exhausted": [],
    "grants": [{
      "id": "opus55-launch-promax-20260921",
      "label": "Claude Opus 5.5 launch: one usage-limit reset for Pro and Max",
      "resets_total": 1, "resets_left": 1,
      "starts_at": "2026-09-22T16:00:00+00:00", "ends_at": "2026-10-22T16:00:00+00:00",
      "clears": ["five_hour","seven_day","seven_day_overage_included"],
      "paused": false, "usable_now": true, "use_requires_limit": false,
      "percent_used": {"five_hour":84,"seven_day":92,"seven_day_overage_included":0},
      "blocking": [], "arm": null }],
    "next_grant_id": "opus55-launch-promax-20260921",
    "weekly_resets_at": "2026-10-02T07:00:00+00:00",
    "cooldown_until": null,
    "event_props": {"surface":"claude_ai","tier":"claude_max_20x"}
  },
  "limits": [{"kind":"session","group":"session","percent":84,"severity":"warning","resets_at":"2026-09-30T12:00:00+00:00"}]
}`

// The grant as Anthropic tells it: one reset, until 22 October, the one
// named next; none shown when it is paused, over, or the account may not
// use resets.
func TestClaudeGrants(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	read := func(edit func(string) string) *claudeGrants {
		t.Helper()
		var data struct {
			Grants *claudeGrants `json:"cedar_ember"`
		}
		if err := json.Unmarshal([]byte(edit(claudeCedarEmber)), &data); err != nil {
			t.Fatal(err)
		}
		return data.Grants
	}
	same := func(s string) string { return s }
	g := read(same)
	r := g.credits(now)
	if r == nil || r.Count != 1 || r.Until == nil || !r.Until.Equal(time.Date(2026, 10, 22, 16, 0, 0, 0, time.UTC)) || r.ByWindow {
		t.Fatalf("credits %+v", r)
	}
	if next, ok := g.next(now); !ok || next.ID != "opus55-launch-promax-20260921" || len(next.Clears) != 3 {
		t.Fatalf("next %+v %v", next, ok)
	}
	for name, edit := range map[string]func(string) string{
		"paused":     func(s string) string { return strings.Replace(s, `"paused": false`, `"paused": true`, 1) },
		"ineligible": func(s string) string { return strings.Replace(s, `"eligible": true`, `"eligible": false`, 1) },
		"used":       func(s string) string { return strings.Replace(s, `"resets_left": 1`, `"resets_left": 0`, 1) },
		"over": func(s string) string {
			return strings.Replace(s, `"2026-10-22T16:00:00+00:00"`, `"2026-09-29T16:00:00+00:00"`, 1)
		},
	} {
		g := read(edit)
		if r := g.credits(now); r != nil {
			t.Errorf("%s: credits %+v", name, r)
		}
		if _, ok := g.next(now); ok {
			t.Errorf("%s: a grant to use", name)
		}
	}
	// held but not usable yet (one that waits for the limit): shown, not spent
	g = read(func(s string) string { return strings.Replace(s, `"usable_now": true`, `"usable_now": false`, 1) })
	if r := g.credits(now); r == nil || r.Count != 1 {
		t.Fatalf("waiting: %+v", r)
	}
	if _, ok := g.next(now); ok {
		t.Fatal("waiting: spent")
	}
	if g := read(func(s string) string {
		return strings.Replace(s, `"next_grant_id": "opus55-launch-promax-20260921"`, `"next_grant_id": null`, 1)
	}); g.credits(now) == nil {
		t.Fatal("no next: count lost")
	} else if _, ok := g.next(now); ok {
		t.Fatal("no next: spent")
	}
}

// claudeResetFake stands in for Anthropic's usage endpoint, which tells of
// the resets only when asked for them as Claude Code asks, the profile,
// and spending one, which refills the windows.
type claudeResetFake struct {
	mu     sync.Mutex
	week   float64 // seven_day used
	left   int     // resets left
	asked  []string
	claims []map[string]string
	heads  []http.Header
	paths  []string
}

func newClaudeResetFake(t *testing.T, week float64) *claudeResetFake {
	t.Helper()
	f := &claudeResetFake{week: week, left: 1}
	// the grant runs out after the test, whenever it runs
	ends := time.Now().Add(20 * 24 * time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/oauth/usage":
			f.asked = append(f.asked, r.URL.RawQuery)
			var body map[string]any
			json.Unmarshal([]byte(strings.Replace(claudeCedarEmber, "2026-10-22T16:00:00+00:00", ends, 1)), &body)
			body["five_hour"] = map[string]any{"utilization": 84, "resets_at": time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)}
			body["seven_day"] = map[string]any{"utilization": f.week, "resets_at": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)}
			ce := body["cedar_ember"].(map[string]any)
			ce["grants"].([]any)[0].(map[string]any)["resets_left"] = f.left
			if r.URL.Query().Get("cedar_ember") != "1" {
				delete(body, "cedar_ember")
			}
			json.NewEncoder(w).Encode(body)
		case r.URL.Path == "/api/oauth/profile":
			io.WriteString(w, `{"account":{"uuid":"u-a","email":"a@example.com"},"organization":{"uuid":"0f1e2d3c-org","organization_type":"claude_max"}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reset_rate_limits"):
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			f.claims, f.heads, f.paths = append(f.claims, in), append(f.heads, r.Header.Clone()), append(f.paths, r.URL.Path)
			if f.left == 0 {
				io.WriteString(w, `{"result":"already_used","reason":"already_used","resets_left":0}`)
				return
			}
			f.left--
			f.week = 0
			io.WriteString(w, `{"result":"reset","reason":null,"resets_left":0,"cleared":["five_hour","seven_day","seven_day_overage_included"],"weekly_resets_at":"2026-10-02T07:00:00+00:00"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	claudeBase = srv.URL // isolate puts it back
	claudeUsage.Lock()
	claudeUsage.m = nil
	claudeUsage.Unlock()
	t.Cleanup(func() { claudeUsage.Lock(); claudeUsage.m = nil; claudeUsage.Unlock() })
	return f
}

func (f *claudeResetFake) claimed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.claims)
}

// claudeSignedInAs signs Claude Code in to a@example.com in a sandbox HOME.
func claudeSignedInAs(t *testing.T) {
	t.Helper()
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour)) // sk-ant-oat01-old
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{
		"emailAddress": "a@example.com", "accountUuid": "u-a", "organizationUuid": "0f1e2d3c-org"}})
}

// The Usage page's card: the Claude account's resets are read with its
// usage, asked for as Claude Code asks, and shown with when the one used
// next runs out.
func TestClaudeResetsOnCard(t *testing.T) {
	claudeSignedInAs(t)
	f := newClaudeResetFake(t, 92)
	q := claudeSubscriptionUsage(context.Background())
	if q.Error != "" || len(q.Windows) < 2 || q.Windows[1].Used != 92 {
		t.Fatalf("usage %+v", q)
	}
	if q.Resets == nil || q.Resets.Count != 1 || q.Resets.Until == nil || q.Resets.Until.Sub(time.Now()) < 19*24*time.Hour {
		t.Fatalf("resets %+v", q.Resets)
	}
	if len(f.asked) != 1 || f.asked[0] != "cedar_ember=1&skip_spend=1" {
		t.Fatalf("asked %q", f.asked)
	}
	b, _ := json.Marshal(q)
	if !strings.Contains(string(b), `"resets":{"count":1,"until":`) {
		t.Fatalf("json %s", b)
	}
}

// Use a reset: the grant Anthropic names next is claimed as Claude Code's
// /limit-reset claims it, for the account's organization, and the windows
// it cleared read as unused at once; with none left, none is claimed.
func TestUseClaudeReset(t *testing.T) {
	claudeSignedInAs(t)
	f := newClaudeResetFake(t, 100)
	out, err := UseClaudeReset(context.Background(), "")
	if err != nil || out.Code != "reset" || out.Windows != 3 || out.Text() != "3 windows started again" {
		t.Fatalf("%+v %v", out, err)
	}
	if f.claimed() != 1 {
		t.Fatalf("claims %d", f.claimed())
	}
	c, h := f.claims[0], f.heads[0]
	if c["program"] != "cedar_ember" || c["grant_id"] != "opus55-launch-promax-20260921" || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(c["request_id"]) || len(c) != 3 {
		t.Fatalf("claim %v", c)
	}
	if f.paths[0] != "/api/organizations/0f1e2d3c-org/reset_rate_limits" || h.Get("Authorization") != "Bearer sk-ant-oat01-old" || h.Get("anthropic-beta") != "oauth-2025-04-20" {
		t.Fatalf("sent to %s with %v", f.paths[0], h)
	}
	q := claudeSubscriptionUsage(context.Background())
	if q.Resets != nil || q.Windows[1].Used != 0 {
		t.Fatalf("after: %+v", q)
	}
	if out, err := UseClaudeReset(context.Background(), "a@example.com"); err != nil || out.Code != "no_credit" || f.claimed() != 1 {
		t.Fatalf("again: %+v %v, %d claims", out, err, f.claimed())
	}
}

// A reset is spent by itself only for an account the user let, only when
// its week is used up, and once in that week.
func TestAutoUseClaudeReset(t *testing.T) {
	claudeSignedInAs(t)
	f := newClaudeResetFake(t, 100)
	if out, err := AutoUseClaudeReset(context.Background(), ""); err != nil || out.Code != "" || f.claimed() != 0 {
		t.Fatalf("not turned on: %+v %v", out, err)
	}
	if err := SetClaudeAutoReset("A@example.com", true); err != nil {
		t.Fatal(err)
	}
	if !AutoResets("claude", "a@example.com") || AutoResets("codex", "a@example.com") {
		t.Fatal("setting read back wrong")
	}
	f.mu.Lock()
	f.week = 92
	f.mu.Unlock()
	claudeAutoReset.Lock()
	claudeAutoReset.path = "" // read afresh, no wait left over
	claudeAutoReset.Unlock()
	if out, err := AutoUseClaudeReset(context.Background(), ""); err != nil || out.Code != "" || f.claimed() != 0 {
		t.Fatalf("week not used up: %+v %v", out, err)
	}
	f.mu.Lock()
	f.week = 100
	f.mu.Unlock()
	claudeAutoReset.Lock()
	claudeAutoReset.path = ""
	claudeAutoReset.Unlock()
	out, err := AutoUseClaudeReset(context.Background(), "")
	if err != nil || out.Code != "reset" || f.claimed() != 1 {
		t.Fatalf("week used up: %+v %v, %d claims", out, err, f.claimed())
	}
	// used up again in the same week, with another reset: kept
	f.mu.Lock()
	f.week, f.left = 100, 1
	f.mu.Unlock()
	claudeAutoReset.Lock()
	claudeAutoReset.path = "" // as a new magpie would
	claudeAutoReset.Unlock()
	if out, err := AutoUseClaudeReset(context.Background(), ""); err != nil || out.Code != "" || f.claimed() != 1 {
		t.Fatalf("second in the week: %+v %v, %d claims", out, err, f.claimed())
	}
}

// A model's own week (Fable's, Opus's) used up doesn't spend a reset:
// only the week every model counts toward.
func TestClaudeWeekUsedUp(t *testing.T) {
	now := time.Now()
	later := now.Add(48 * time.Hour)
	week := 7 * 24 * time.Hour
	fable := QuotaWindow{Name: "7 days · Fable", Span: week, Used: 100, ResetsAt: &later, Model: "fable"}
	all := QuotaWindow{Name: "7 days", Span: week, Used: 60, ResetsAt: &later}
	if claudeWeekUsedUp([]QuotaWindow{all, fable}, now) != nil {
		t.Fatal("a model's week counted")
	}
	all.Used = 100
	if claudeWeekUsedUp([]QuotaWindow{all, fable}, now) == nil {
		t.Fatal("the week not counted")
	}
}
