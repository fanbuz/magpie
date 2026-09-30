package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Zed's accounts on @magpie-community/opencode-zed-auth: the access token
// as the oauth entry's access, the rest the calls need as its refresh
// (Zed has no refresh token: the pair lasts until Zed refuses it).
func init() {
	movers["zed"] = &mover{
		pkg:    "@magpie-community/opencode-zed-auth",
		agents: []string{"zed"},
		out: func() ([]Moving, error) {
			var out []Moving
			for _, l := range zedLogins() {
				c := l.creds
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Auth: map[string]any{
					"type":      "oauth",
					"access":    c.Access,
					"refresh":   jsonText(map[string]any{"userId": c.UserID, "systemId": c.SystemID, "org": c.Org, "plan": c.Plan, "login": c.Login, "name": c.Name}),
					"expires":   0,
					"accountId": l.User,
				}})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			var r struct {
				UserID   any    `json:"userId"`
				SystemID string `json:"systemId"`
				Org      string `json:"org"`
				Plan     string `json:"plan"`
				Login    string `json:"login"`
				Name     string `json:"name"`
			}
			access := str(auth["access"])
			if json.Unmarshal([]byte(str(auth["refresh"])), &r) != nil || r.UserID == nil || access == "" {
				return ls, "", errors.New("Zed: an unreadable plugin sign-in")
			}
			if user == "" {
				user = str(auth["accountId"])
			}
			i := backInto(&ls, "zed", user)
			c, _ := zedSaved(ls[i])
			// the plugin may keep the id as the number Zed gives
			c.UserID, c.Access, c.SystemID, c.Org, c.Plan = strings.Trim(jsonText(r.UserID), `"`), access, r.SystemID, r.Org, r.Plan
			if r.Login != "" {
				c.Login = r.Login
			}
			if r.Name != "" {
				c.Name = r.Name
			}
			b, err := json.Marshal(c)
			if err != nil {
				return ls, "", err
			}
			ls[i].Auth, ls[i].Lapsed, ls[i].Seen = b, "", time.Now().UTC().Truncate(time.Second)
			return ls, ls[i].User, nil
		},
	}
}

// backInto is the index in ls of agent's saved account user, added (on)
// when there is none.
func backInto(ls *[]savedLogin, agent, user string) int {
	for i, l := range *ls {
		if l.Agent == agent && strings.EqualFold(l.User, user) && !l.own() {
			return i
		}
	}
	*ls = append(*ls, savedLogin{Agent: agent, User: user, On: true, Seen: time.Now().UTC().Truncate(time.Second)})
	return len(*ls) - 1
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
