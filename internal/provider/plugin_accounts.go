package provider

import (
	"context"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's provider is signed in to as many times as the user likes, as
// a built-in subscription is: plugin-auth.json keeps each account's
// sign-in (under the provider's id, then id#slot), logins.json which is
// first and which are on, as it does for the built-ins, the account's key
// in its Home.

// pluginAgent is what logins.json lists a plugin provider's accounts as.
func pluginAgent(pp plugin.Provider) string { return "plugin:" + pp.ID }

// pluginLabels names each account: its id, else the end of its key, else
// what it signed in with; a name two share gets the account's slot.
func pluginLabels(pp plugin.Provider) map[string]string {
	out := map[string]string{}
	seen := map[string]int{}
	for _, a := range pp.Accounts {
		l := a.AccountID
		if l == "" && a.Hint != "" {
			l = "API key …" + a.Hint
		}
		if l == "" {
			l = map[string]string{"api": "API key", "oauth": "Signed in"}[a.Type]
		}
		if l == "" {
			l = "Signed in"
		}
		out[a.Key] = l
		seen[strings.ToLower(l)]++
	}
	for _, a := range pp.Accounts {
		if seen[strings.ToLower(out[a.Key])] > 1 {
			if _, slot, ok := strings.Cut(a.Key, "#"); ok {
				out[a.Key] += " (" + slot + ")"
			}
		}
	}
	return out
}

type pluginLogin struct {
	sideLogin
	acct plugin.Account
}

// pluginLogins are the provider's accounts, the first in use first. What
// plugin-auth.json keeps is the truth: logins.json follows it.
func pluginLogins(pp plugin.Provider) []pluginLogin {
	agent := pluginAgent(pp)
	labels := pluginLabels(pp)
	byKey := map[string]plugin.Account{}
	for _, a := range pp.Accounts {
		byKey[a.Key] = a
	}
	loginsMu.Lock()
	ls := readLogins()
	changed := false
	have := map[string]bool{}
	keep := ls[:0]
	for _, l := range ls {
		if l.Agent == agent {
			if _, ok := byKey[l.Home]; !ok || have[l.Home] {
				changed = true // signed out, or listed twice
				continue
			}
			have[l.Home] = true
			if l.User != labels[l.Home] {
				l.User, changed = labels[l.Home], true
			}
		}
		keep = append(keep, l)
	}
	ls = keep
	for _, a := range pp.Accounts {
		if !have[a.Key] {
			ls = append(ls, savedLogin{Agent: agent, User: labels[a.Key], Home: a.Key, On: true, Seen: time.Now().UTC().Truncate(time.Second)})
			changed = true
		}
	}
	if changed {
		_ = writeLogins(ls)
	}
	loginsMu.Unlock()
	var out []pluginLogin
	for _, l := range sideLogins(agent, "", func(l savedLogin) bool { _, ok := byKey[l.Home]; return ok }) {
		out = append(out, pluginLogin{l, byKey[l.saved.Home]})
	}
	return out
}

func pluginSide(pp plugin.Provider) []sideLogin {
	var out []sideLogin
	for _, l := range pluginLogins(pp) {
		out = append(out, l.sideLogin)
	}
	return out
}

// pluginOfAgent is the plugin provider the accounts page names by its
// magpie id.
func pluginOfAgent(agent string) (plugin.Provider, bool) {
	if strings.HasPrefix(agent, "plugin:") {
		id := strings.TrimPrefix(agent, "plugin:")
		for _, pp := range plugin.Cached() {
			if pp.ID == id {
				return pp, true
			}
		}
		return plugin.Provider{}, false
	}
	return PluginOf(agent)
}

func pluginLoginList(pp plugin.Provider) []Login { return loginsOf(pluginSide(pp)) }

func switchPluginLogin(pp plugin.Provider, user string) error {
	return switchSideLogin(pluginAgent(pp), user, pluginSide(pp))
}

func setPluginLoginOn(pp plugin.Provider, user string, on bool) error {
	return setSideLoginOn(pluginAgent(pp), user, on, pluginSide(pp))
}

// forgetPluginLogin signs the account out: its sign-in is magpie's own.
func forgetPluginLogin(pp plugin.Provider, user string) error {
	var err error
	ferr := forgetSideLogin(pluginAgent(pp), user, pluginSide(pp), func(l savedLogin) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = plugin.SignOut(ctx, pp.ID, l.Home)
	})
	if ferr != nil {
		return ferr
	}
	return err
}

// pluginAlsoOn is the provider's accounts in use behind the first.
func pluginAlsoOn(pp plugin.Provider) []Provider {
	var out []Provider
	for _, l := range pluginLogins(pp) {
		if !l.Active && l.On {
			out = append(out, pluginProvider(pp, l))
		}
	}
	return out
}

// keepPluginPlan keeps the plan an account's allowance told on its row,
// for the accounts list to show it as a built-in's.
func keepPluginPlan(pp plugin.Provider, key, plan string) {
	if plan == "" {
		return
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i, l := range ls {
		if l.Agent == pluginAgent(pp) && l.Home == key {
			if l.Plan != plan {
				ls[i].Plan = plan
				_ = writeLogins(ls)
			}
			return
		}
	}
}
