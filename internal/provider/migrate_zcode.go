package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ZCode's accounts on @magpie-community/opencode-zcode-auth: the plan's
// key (else ZCode's session token) as the oauth entry's access, the rest
// the requests need as its refresh, as the plugin's own sign-in keeps
// them. Nothing here rotates: a key lasts until it is deleted.
//
// ZCode's own sign-in is kept encrypted, where the plugin can't read it,
// so it goes as a copy of what magpie reads now. A key or a team seat
// lasts, and the copy with it; ZCode's session token alone (the Start
// Plan) is renewed by ZCode, and a copy would lapse behind it, so such an
// account stops the move and the built-in carries on.
func init() {
	movers["zcode"] = &mover{
		pkg:    "@magpie-community/opencode-zcode-auth",
		agents: []string{"zcode"},
		out: func() ([]Moving, error) {
			var out []Moving
			for _, l := range zcodeLogins() {
				k := l.key
				if l.Own && k.Key == "" && !k.team() {
					return nil, errors.New("ZCode's own sign-in has no coding plan key, only ZCode's session, which the plugin can't follow")
				}
				// the own account's copy goes back to nothing: ZCode keeps it
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Own: l.Own, Auth: zcodeOut(l.User, l.Plan, k)})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			var s struct {
				Site, Device, Key, Base, JWT, Token, Org, Project, Plan string
			}
			if json.Unmarshal([]byte(str(auth["refresh"])), &s) != nil {
				// a key signed in to in the plugin
				if str(auth["type"]) != "api" || str(auth["key"]) == "" {
					return ls, "", errors.New("ZCode: an unreadable plugin sign-in")
				}
				s.Key = str(auth["key"])
				if m, _ := auth["metadata"].(map[string]any); m != nil {
					s.Site = str(m["site"])
				}
			} else if s.Key == "" && s.JWT == "" {
				s.Key = str(auth["access"])
			}
			k := zcodeKey{Key: s.Key, Base: s.Base, JWT: s.JWT, Token: s.Token, Org: s.Org, Project: s.Project}
			if k.Base == "" {
				k.Base = ZCodeZaiBase
				if s.Site == "bigmodel" {
					k.Base = ZCodeBigModelBase
				}
			}
			if k.Key == "" && k.JWT == "" && !k.team() {
				return ls, "", errors.New("ZCode: an unreadable plugin sign-in")
			}
			if user == "" {
				user = firstNonEmpty(str(auth["accountId"]), "ZCode")
			}
			i := backInto(&ls, "zcode", user)
			b, err := json.Marshal(k)
			if err != nil {
				return ls, "", err
			}
			ls[i].Auth, ls[i].Lapsed, ls[i].Seen = b, "", time.Now().UTC().Truncate(time.Second)
			if s.Plan != "" {
				ls[i].Plan = s.Plan
			}
			return ls, ls[i].User, nil
		},
	}
}

// zcodeOut is a ZCode account as the plugin's sign-in keeps one.
func zcodeOut(user, plan string, k zcodeKey) map[string]any {
	site := "zai"
	if strings.Contains(k.Base, "bigmodel.cn") {
		site = "bigmodel"
	}
	state := map[string]any{"site": site, "device": zcodeDeviceMid(), "base": k.Base}
	for name, v := range map[string]string{"key": k.Key, "jwt": k.JWT, "token": k.Token, "org": k.Org, "project": k.Project, "plan": plan} {
		if v != "" {
			state[name] = v
		}
	}
	expires := int64(0)
	if k.Key == "" && !k.team() {
		if exp, _ := jwtClaims(k.JWT)["exp"].(float64); exp > 0 {
			expires = int64(exp) * 1000
		}
	}
	return map[string]any{"type": "oauth", "access": firstNonEmpty(k.Key, k.JWT), "refresh": jsonText(state), "expires": expires, "accountId": user}
}
