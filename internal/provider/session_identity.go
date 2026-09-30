package provider

// Local identities for associating historical session metadata. No credential
// refresh, sign-in discovery command or login-store write is needed here.
import (
	"cmp"
	"encoding/json"
	"path/filepath"

	"github.com/yetone/magpie/internal/filememo"
)

// SessionIdentity is an account's public identity, without its credentials.
type SessionIdentity struct {
	Agent, AccountID, UserID, OrganizationID, User string
}

// SessionIdentities reads the agent's current identity and remembered identities.
// Callers must match the IDs recorded in a session; presence alone is not evidence
// that the account served that session's requests.
func SessionIdentities(codexDir string) []SessionIdentity {
	var out []SessionIdentity
	appendCodex := func(b []byte) {
		var a codexAuth
		if json.Unmarshal(b, &a) != nil || a.AuthMode == "apikey" {
			return
		}
		claims := jwtClaims(a.Tokens.IDToken)
		id := cmp.Or(a.Tokens.AccountID, claimString(claims, "https://api.openai.com/auth", "chatgpt_account_id"))
		if user := codexUser(claims); id != "" && user != "" {
			out = append(out, SessionIdentity{Agent: "codex", AccountID: id,
				UserID: cmp.Or(claimString(claims, "https://api.openai.com/auth", "chatgpt_user_id"), claimString(claims, "https://api.openai.com/auth", "user_id")), User: user})
		}
	}
	appendClaude := func(b []byte) {
		var a struct {
			Account string `json:"accountUuid"`
			Org     string `json:"organizationUuid"`
			Email   string `json:"emailAddress"`
		}
		if json.Unmarshal(b, &a) == nil && a.Account != "" && a.Email != "" {
			out = append(out, SessionIdentity{Agent: "claude", AccountID: a.Account, OrganizationID: a.Org, User: a.Email})
		}
	}
	// Cache only the identity parse, not another copy of the credential blob.
	current, _ := filememo.Read("session codex identity", filepath.Join(codexDir, "auth.json"), func(b []byte) ([]SessionIdentity, error) {
		appendCodex(b)
		ids := out
		out = nil
		return ids, nil
	})
	out = append(out, current...)
	if acct, ok := claudeProfileAccount(); ok {
		b, _ := json.Marshal(acct)
		appendClaude(b)
	}
	for _, l := range readLogins() {
		switch l.Agent {
		case "codex":
			appendCodex(l.Auth)
		case "claude":
			appendClaude(l.Profile)
		}
	}
	return out
}
