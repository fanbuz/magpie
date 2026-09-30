package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionIdentitiesReadOnly(t *testing.T) {
	home := signIn(t)
	path := filepath.Join(home, ".codex", "auth.json")
	before, _ := os.ReadFile(path)
	oldAuth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     fakeJWT(map[string]any{"email": "old@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "old-account", "chatgpt_user_id": "old-user"}}),
		"access_token": "secret-access", "refresh_token": "secret-refresh", "account_id": "old-account"}})
	profile := json.RawMessage(`{"accountUuid":"claude-old","organizationUuid":"org-old","emailAddress":"claude-old@example.com"}`)
	if err := writeLogins([]savedLogin{{Agent: "codex", User: "old@example.com", Auth: oldAuth}, {Agent: "claude", User: "claude-old@example.com", Profile: profile}}); err != nil {
		t.Fatal(err)
	}
	ids := SessionIdentities(filepath.Dir(path))
	byID := map[string]SessionIdentity{}
	for _, id := range ids {
		byID[id.AccountID] = id
	}
	if byID["acct-1"].User != "me@example.com" || byID["old-account"].UserID != "old-user" || byID["claude-old"].OrganizationID != "org-old" {
		t.Fatalf("identities %+v", ids)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("identity lookup changed the agent's auth file")
	}
	b, _ := json.Marshal(ids)
	if strings.Contains(string(b), "secret-") {
		t.Fatal("credentials leaked into identity metadata")
	}
}

func TestSubscriptionIdentitiesOnlyContainUsableAccounts(t *testing.T) {
	home := signIn(t)
	auth := func(account, user, email string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": account, "access_token": "test", "id_token": fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account, "chatgpt_user_id": user}})}})
		return b
	}
	if err := writeLogins([]savedLogin{
		{Agent: "codex", User: "me@example.com", On: true, Auth: auth("stale-workspace", "old-user", "me@example.com")},
		{Agent: "codex", User: "enabled@example.com", On: true, Auth: auth("enabled", "u-enabled", "enabled@example.com")},
		{Agent: "codex", User: "disabled@example.com", Auth: auth("disabled", "u-disabled", "disabled@example.com")},
	}); err != nil {
		t.Fatal(err)
	}
	alternate := filepath.Join(home, "alternate")
	if err := os.MkdirAll(alternate, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alternate, "auth.json"), auth("foreign", "u-foreign", "foreign@example.com"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", alternate)
	p, ok := codexAccount(home)
	if !ok {
		t.Fatal("missing current subscription")
	}
	ids := p.SessionIdentities()
	found := map[string]bool{}
	for _, id := range ids {
		found[id.AccountID] = true
	}
	if len(ids) != 2 || !found["acct-1"] || !found["enabled"] {
		t.Fatalf("wrong subscription membership: %+v", ids)
	}
}
