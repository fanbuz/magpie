package usage

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// Vendor identities for session calls, separate from a configured gateway
// subscription provider whose account can change.
const (
	SessionOpenAIProvider    = "session-openai"
	SessionAnthropicProvider = "session-anthropic"
)

type sessionAttribution struct {
	provider, user, account string
}

type codexSessionProvider struct {
	BaseURL            string `toml:"base_url"`
	RequiresOpenAIAuth bool   `toml:"requires_openai_auth"`
}

type desktopSessionInfo struct {
	Session string `json:"cliSessionId"`
	Email   string `json:"emailAddress"`
}

type desktopSessionIdentity struct {
	account, org, email string
	official            bool
}

type sessionResolver struct {
	identities []provider.SessionIdentity
	providers  map[string]codexSessionProvider
	desktop    map[string]desktopSessionIdentity // absolute metadata file
	bySession  map[string][]desktopSessionIdentity
	emails     map[string]string // exact account/organization identity
	roots      []string
}

func newSessionResolver(logs []sessions.Call) *sessionResolver {
	r := &sessionResolver{}
	if len(logs) == 0 {
		return r
	}
	r.identities = provider.SessionIdentities(sessions.CodexDir())
	r.providers, _ = filememo.Read("session codex providers", filepath.Join(sessions.CodexDir(), "config.toml"), func(b []byte) (map[string]codexSessionProvider, error) {
		var c struct {
			Providers map[string]codexSessionProvider `toml:"model_providers"`
		}
		err := toml.Unmarshal(b, &c)
		return c.Providers, err
	})
	r.desktop = map[string]desktopSessionIdentity{}
	r.bySession = map[string][]desktopSessionIdentity{}
	r.emails = map[string]string{}
	for _, id := range r.identities {
		if id.Agent == "claude" {
			r.noteEmail(id.AccountID, id.OrganizationID, id.User)
		}
	}
	r.roots = sessions.DesktopDataDirs()
	for _, root := range r.roots {
		for _, kind := range []string{"local-agent-mode-sessions", "claude-code-sessions"} {
			files, _ := filepath.Glob(filepath.Join(root, kind, "*", "*", "local_*.json"))
			for _, path := range files {
				meta, err := filememo.Read("session desktop identity", path, func(b []byte) (desktopSessionInfo, error) {
					var info desktopSessionInfo
					err := json.Unmarshal(b, &info)
					return info, err
				})
				if err != nil {
					continue
				}
				rel, _ := filepath.Rel(root, path)
				parts := strings.Split(rel, string(filepath.Separator))
				id := desktopSessionIdentity{account: parts[1], org: parts[2], email: meta.Email,
					official: !strings.Contains(filepath.Base(root), "-3p") && uuidIdentity(parts[1]) && uuidIdentity(parts[2])}
				r.desktop[path] = id
				if meta.Session != "" {
					r.bySession[meta.Session] = append(r.bySession[meta.Session], id)
				}
				if id.official {
					r.noteEmail(id.account, id.org, id.email)
				}
			}
		}
	}
	return r
}

func uuidIdentity(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

func (r *sessionResolver) noteEmail(account, org, email string) {
	if account == "" || email == "" {
		return
	}
	key := account + "/" + org
	if old, ok := r.emails[key]; ok && old != email {
		r.emails[key] = "" // conflicting identities never pick an arbitrary email
	} else {
		r.emails[key] = email
	}
}

func (r *sessionResolver) codexUser(c sessions.Call) string {
	if c.AccountID == "" {
		return ""
	}
	user := ""
	for _, id := range r.identities {
		if id.Agent != "codex" || id.AccountID != c.AccountID || c.UserID != "" && id.UserID != c.UserID {
			continue
		}
		if user != "" && user != id.User {
			return "" // a workspace can have more than one signed-in member
		}
		user = id.User
	}
	return user
}

func officialOpenAI(base string) bool {
	if base == "" {
		return true // the agent's built-in OpenAI backend
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	switch u.Hostname() {
	case "api.openai.com", "chatgpt.com", "chat.openai.com":
		return true
	}
	return false
}

func (r *sessionResolver) resolve(c sessions.Call) sessionAttribution {
	unknown := sessionAttribution{provider: UnknownProvider}
	if c.Agent == "codex" {
		user := r.codexUser(c)
		unknown.account = user
		// The file names the provider id, not its address: that id is judged by
		// today's configuration, for the built-in "openai" and a named one alike.
		// A named provider is OpenAI's when it points at OpenAI, or signs in with
		// the ChatGPT account and leaves the address to Codex's default.
		p, ok := r.providers[c.Upstream]
		if c.Upstream == "openai" && (!ok || officialOpenAI(p.BaseURL)) ||
			c.Upstream != "" && ok && officialOpenAI(p.BaseURL) && (p.BaseURL != "" || p.RequiresOpenAIAuth) {
			return sessionAttribution{provider: SessionOpenAIProvider, account: user}
		}
		// A creator identity proves who owns the session, not which service
		// handled a call.
		return unknown
	}
	if c.Agent != "claude" && c.Agent != "claude-desktop" {
		return unknown
	}
	var id desktopSessionIdentity
	found := false
	for _, root := range r.roots {
		rel, err := filepath.Rel(root, c.File)
		if err != nil {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) > 4 && parts[0] == "local-agent-mode-sessions" && strings.HasPrefix(parts[3], "local_") && parts[4] == ".claude" {
			path := filepath.Join(root, filepath.Join(parts[:4]...)) + ".json"
			id, found = r.desktop[path]
			break
		}
	}
	if !found {
		if ids := r.bySession[c.Session]; len(ids) == 1 {
			id, found = ids[0], true
		}
	}
	if !found {
		return unknown
	}
	if id.email == "" {
		id.email = r.emails[id.account+"/"+id.org]
	}
	if !id.official {
		return sessionAttribution{provider: UnknownProvider, account: id.email}
	}
	return sessionAttribution{provider: SessionAnthropicProvider, user: id.email}
}
