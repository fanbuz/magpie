package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// claudeResetAccounts signs Claude Code in to me@example.com and keeps
// spare@example.com too, both out of their allowance, with a claude CLI
// that answers me's turns again once Anthropic's fake (which it returns)
// took one of me's resets.
func claudeResetAccounts(t *testing.T) (claims func() []map[string]string, log string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	far := time.Now().Add(24 * time.Hour).UnixMilli()
	oauth := func(tok string) map[string]any {
		return map[string]any{"claudeAiOauth": map[string]any{"accessToken": tok, "refreshToken": "r-" + tok, "expiresAt": far, "subscriptionType": "max"}}
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(oauth("tok-me")), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "me@example.com", "accountUuid": "u-me"}}), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "claude", "user": "spare@example.com", "plan": "max", "on": true, "seen": time.Now(), "auth": oauth("tok-spare")},
	}), 0o600)

	dir := t.TempDir()
	marker := filepath.Join(dir, "reset")
	log = filepath.Join(dir, "log")
	var mu sync.Mutex
	var got []map[string]string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/oauth/usage":
			week := 100
			if _, err := os.Stat(marker); err == nil {
				week = 0
			}
			later := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }
			json.NewEncoder(w).Encode(map[string]any{
				"five_hour": map[string]any{"utilization": week, "resets_at": later(2 * time.Hour)},
				"seven_day": map[string]any{"utilization": week, "resets_at": later(48 * time.Hour)},
				"cedar_ember": map[string]any{"eligible": true, "next_grant_id": "opus55-launch-promax-20260921",
					"grants": []any{map[string]any{"id": "opus55-launch-promax-20260921", "resets_left": 1, "usable_now": true, "paused": false,
						"ends_at": later(20 * 24 * time.Hour), "clears": []string{"five_hour", "seven_day", "seven_day_overage_included"}}}}})
		case r.URL.Path == "/api/oauth/profile":
			io.WriteString(w, `{"organization":{"uuid":"org-me"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/org-me/reset_rate_limits":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			mu.Lock()
			got = append(got, in)
			mu.Unlock()
			os.WriteFile(marker, nil, 0o644)
			io.WriteString(w, `{"result":"reset","resets_left":0,"cleared":["five_hour","seven_day","seven_day_overage_included"]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(api.Close)
	t.Cleanup(provider.PointClaudeAt(api.URL))

	script := `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}'; exit 0; fi
echo "${CLAUDE_CODE_OAUTH_TOKEN:-own}" >> ` + log + `
while read -r line; do
  if [ -z "$CLAUDE_CODE_OAUTH_TOKEN" ] && [ -f ` + marker + ` ]; then
    echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}'
    echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
    echo '{"type":"stream_event","event":{"type":"message_stop"}}'
    echo '{"type":"result","subtype":"success","is_error":false,"result":"pong"}'
  else
    echo '{"type":"result","subtype":"success","is_error":true,"result":"You'"'"'ve hit your limit · resets 3am"}'
  fi
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	return func() []map[string]string {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]string(nil), got...)
	}, log
}

const claudeResetAsk = `{"model":"claude/claude-sonnet-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"ping"}]}`

// Both Claude accounts out of their allowance, the one Claude Code is
// signed in to spending its resets by itself (a Max subscriber on
// Discord, holding Opus 5.5's launch reset): the reset Anthropic names
// next is taken, and the turn goes through on it.
func TestClaudeAutoReset(t *testing.T) {
	claims, log := claudeResetAccounts(t)
	if err := provider.SetClaudeAutoReset("Me@example.com", true); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := sendTo(s, "/v1/messages", claudeResetAsk)
	b, _ := os.ReadFile(log)
	runs := strings.Join(strings.Fields(string(b)), ",")
	if code != 200 || !strings.Contains(body, "pong") || !strings.HasSuffix(runs, ",own") || !strings.Contains(runs, "tok-spare") {
		t.Fatalf("status %d: %s (runs %s)", code, body, runs)
	}
	if c := claims(); len(c) != 1 || c[0]["program"] != "cedar_ember" || c[0]["grant_id"] != "opus55-launch-promax-20260921" || c[0]["request_id"] == "" {
		t.Fatalf("claims %v", c)
	}
	if got := resetsTraced(s); len(got) != 1 || got[0] != "me@example.com: 3 windows started again" {
		t.Fatalf("traced %v", got)
	}
	// the Routing page tells it as a Claude reset
	for _, try := range lastRoute(s).Tries {
		if try.Reset != nil && try.Reset.Agent != "claude" {
			t.Fatalf("reset traced as %q's", try.Reset.Agent)
		}
	}
}

// Without the user's leave, no reset is spent: the limit stands.
func TestClaudeAutoResetNot(t *testing.T) {
	claims, _ := claudeResetAccounts(t)
	s := New()
	code, body := sendTo(s, "/v1/messages", claudeResetAsk)
	if code != 429 || len(claims()) != 0 || len(resetsTraced(s)) != 0 {
		t.Fatalf("status %d: %s, claims %v", code, body, claims())
	}
}
