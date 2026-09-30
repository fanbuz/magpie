package agent

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// Claude Code reads its endpoint from the `env` block of settings.json.
// Pointing ANTHROPIC_BASE_URL at the gateway and naming a catalog model in
// ANTHROPIC_MODEL (and the aliases opus/sonnet/haiku resolve through) is
// all it takes to run it on any provider.

// env vars magpie sets while routing through the gateway.
var claudeEnv = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}

// claudeTiers are the aliases Claude Code resolves (/model opus, a
// subagent's "model: haiku", …), each of which can have a model of its own.
// A tier that has none follows the main model.
var claudeTiers = []string{"opus", "sonnet", "haiku", "fable"}

// claudeEfforts are the levels Claude Code starts with: settings.json's
// effortLevel takes the first four, max is claudeEffortEnv.
var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// claudeEffortEnv is the effort every Claude Code session asks for, max
// among them.
const claudeEffortEnv = "CLAUDE_CODE_EFFORT_LEVEL"

// claudeContextEnv is the context window Claude Code takes a model it
// doesn't know for (any but Claude's own names); without it, 200K. Its
// auto-compact window (CLAUDE_CODE_AUTO_COMPACT_WINDOW, the autoCompactWindow
// setting) is only ever the smaller of its own value and this one, so this
// is what tells it where a 128K or a 400K model runs out. A name marked [1m]
// is 1M whatever it says.
const claudeContextEnv = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"

func tierEnv(tier string) string { return "ANTHROPIC_DEFAULT_" + strings.ToUpper(tier) + "_MODEL" }

func claude(home string) *Agent { return claudeIn(here(home)) }

// claudeIn is Claude Code as it lives at a place: this machine's home, or
// a WSL distro's (see wsl.go), its settings.json naming the gateway as it
// reaches it from there.
func claudeIn(at place) *Agent {
	path := filepath.Join(at.home, ".claude", "settings.json")
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	model := jsonGet(path, "model")
	routed := func() bool { return env("ANTHROPIC_BASE_URL") == at.gw() }

	// the value shown: the catalog ref while routed, else Claude's own model.
	get := func() string {
		if routed() {
			if m := env("ANTHROPIC_MODEL"); m != "" {
				return m
			}
		}
		return model()
	}
	// the context window magpie last wrote, so a value the user wrote is
	// never taken for magpie's: that one is left as it is
	windowKey := at.key("claude.context_tokens")
	windowOurs := func() bool {
		cur := env(claudeContextEnv)
		return cur != "" && cur == stashLoad()[windowKey]
	}
	dropWindow := func() error {
		defer forget(windowKey)
		if windowOurs() {
			return edit.DelJSON(path, "env."+claudeContextEnv)
		}
		return nil
	}
	var writeTiers func(main string, tiers map[string]string) error
	set := func(v string) error {
		if v == "" {
			// Claude Code as installed: Anthropic's own endpoint and model
			keys := []string{"model"}
			for _, k := range claudeEnv {
				keys = append(keys, "env."+k)
			}
			forget(at.key("claude.model"), at.key("claude.base_url"), at.key("claude.auth_token"))
			if err := dropWindow(); err != nil {
				return err
			}
			return edit.DelJSON(path, keys...)
		}
		if isMagpie(v) {
			if !routed() {
				stash(map[string]string{
					at.key("claude.model"):      model(),
					at.key("claude.base_url"):   env("ANTHROPIC_BASE_URL"),
					at.key("claude.auth_token"): env("ANTHROPIC_AUTH_TOKEN"),
				})
			}
			// tiers that followed the old model follow the new one; the
			// ones given a model of their own keep it
			tiers := map[string]string{}
			for _, t := range claudeTiers {
				tiers[t] = v
				if w := env(tierEnv(t)); routed() && w != "" && w != env("ANTHROPIC_MODEL") && isMagpie(w) {
					tiers[t] = w
				}
			}
			return writeTiers(v, tiers)
		}
		if err := dropWindow(); err != nil {
			return err
		}
		if routed() {
			keys := make([]string, len(claudeEnv))
			for i, k := range claudeEnv {
				keys[i] = "env." + k
			}
			if err := edit.DelJSON(path, keys...); err != nil {
				return err
			}
			unstash(at.key("claude.model"))
			var back []edit.KV
			if u := unstash(at.key("claude.base_url")); u != "" {
				back = append(back, edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: u})
			}
			if t := unstash(at.key("claude.auth_token")); t != "" {
				back = append(back, edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: t})
			}
			if len(back) > 0 {
				if err := edit.SetJSON(path, back...); err != nil {
					return err
				}
			}
		}
		return edit.SetJSON(path, edit.KV{Path: "model", Value: v})
	}

	// writeTiers routes Claude Code through the gateway with main as its
	// model and each tier on the model given.
	writeTiers = func(main string, tiers map[string]string) error {
		// a 1M model goes in marked [1m] however it was named, or Claude
		// Code takes it for 200K
		mark := claude1M()
		main = mark(main)
		for t, v := range tiers {
			tiers[t] = mark(v)
		}
		kvs := []edit.KV{
			{Path: "env.ANTHROPIC_BASE_URL", Value: at.gw()},
			{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: gateway.Token},
			{Path: "env.ANTHROPIC_MODEL", Value: main},
			{Path: "env.ANTHROPIC_SMALL_FAST_MODEL", Value: tiers["haiku"]},
			{Path: "model", Value: main},
		}
		same := true
		for _, t := range claudeTiers {
			kvs = append(kvs, edit.KV{Path: "env." + tierEnv(t), Value: tiers[t]})
			same = same && tiers[t] == main
		}
		if !same {
			// one model for every subagent would override the tiers they ask for
			if err := edit.DelJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL"); err != nil {
				return err
			}
		} else {
			kvs = append(kvs, edit.KV{Path: "env.CLAUDE_CODE_SUBAGENT_MODEL", Value: main})
		}
		// the new model's window replaces the old one's, when magpie knows
		// it; one the user set is theirs
		if env(claudeContextEnv) == "" || windowOurs() {
			if w := claudeWindow(main, tiers); w > 0 {
				kvs = append(kvs, edit.KV{Path: "env." + claudeContextEnv, Value: strconv.Itoa(w)})
				stash(map[string]string{windowKey: strconv.Itoa(w)})
			} else if err := dropWindow(); err != nil {
				return err
			}
		} else {
			forget(windowKey)
		}
		return edit.SetJSON(path, kvs...)
	}

	fields := []Field{{
		Key: "model", Label: "model",
		Get: get,
		Set: set,
		Options: func(map[string]string) []Option {
			name := "Claude Code"
			if u := env("ANTHROPIC_BASE_URL"); u != "" && !routed() {
				name += " · " + hostOf(u)
			}
			return append(group(name, claudeOwn()), claudeViaMagpie()...)
		},
	}, {
		// the effort Claude Code starts with, as its /effort saves it;
		// settings.json keeps low to xhigh (/effort max lasts a session
		// only), so max is CLAUDE_CODE_EFFORT_LEVEL in its env, which every
		// session starts with and asks for
		Key: "effort", Label: "effort",
		Get: func() string { return cmp.Or(env(claudeEffortEnv), jsonGet(path, "effortLevel")()) },
		Set: func(v string) error {
			if v != "" && !contains(claudeEfforts, v) {
				return fmt.Errorf("Claude Code keeps an effort of %s, not %q", strings.Join(claudeEfforts, ", "), v)
			}
			if v == "max" {
				return edit.SetJSON(path, edit.KV{Path: "env." + claudeEffortEnv, Value: v})
			}
			if env(claudeEffortEnv) != "" {
				if err := edit.DelJSON(path, "env."+claudeEffortEnv); err != nil {
					return err
				}
			}
			return jsonSet(path, "effortLevel")(v)
		},
		Options: func(map[string]string) []Option {
			return static(claudeEfforts...)
		},
	}}
	for _, tier := range claudeTiers {
		fields = append(fields, Field{
			Key: tier, Label: tier, Quiet: true,
			// empty while the tier follows the main model
			Get: func() string {
				if w := env(tierEnv(tier)); routed() && w != env("ANTHROPIC_MODEL") {
					return w
				}
				return ""
			},
			Set: func(v string) error {
				if !routed() {
					if v == "" {
						return nil
					}
					return fmt.Errorf("pick a model through magpie for Claude Code first; %s can then have its own", tier)
				}
				if v != "" && !isMagpie(v) {
					return fmt.Errorf("%s: %q is not a model magpie serves", tier, v)
				}
				main := env("ANTHROPIC_MODEL")
				tiers := map[string]string{}
				for _, t := range claudeTiers {
					tiers[t] = env(tierEnv(t))
					if tiers[t] == "" {
						tiers[t] = main
					}
				}
				tiers[tier] = v
				if v == "" {
					tiers[tier] = main
				}
				return writeTiers(main, tiers)
			},
			Options: func(map[string]string) []Option {
				if !routed() {
					return nil
				}
				return claudeViaMagpie()
			},
		})
	}

	return &Agent{
		ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Aliases: []string{"cc", "claude-code"},
		UA:  []string{"claude-cli", "claude-code"},
		Bin: "claude", Dir: filepath.Dir(path), Path: path,
		Fields: fields,
		Check: func() string {
			if !isMagpie(get()) {
				return ""
			}
			// an administrator's settings win over the user's
			managed := claudeManaged()
			if at.sys != nil {
				managed = at.sys("/etc/claude-code/managed-settings.json")
			}
			if u, _ := edit.GetJSON(managed, "env.ANTHROPIC_BASE_URL"); u != "" && u != at.gw() {
				return "Claude Code's managed settings (" + at.native(managed) + ") set ANTHROPIC_BASE_URL to " + u + ", which wins over magpie's"
			}
			return wiringOff("Claude Code", path, func(k string) (string, bool) { return edit.GetJSON(path, "env."+k) },
				"ANTHROPIC_BASE_URL", at.gw(), "ANTHROPIC_AUTH_TOKEN", gateway.Token)
		},
		// every prompt typed into Claude Code goes into history.jsonl
		LastUsed: func() time.Time {
			return lastJSONLTime(filepath.Join(filepath.Dir(path), "history.jsonl"), "timestamp", "display")
		},
	}
}

// claudeOwn is Anthropic's models as Claude Code takes them: only the
// catalog's; Claude Code's own short aliases are not something any API
// lists, and a compiled-in copy would just go stale.
func claudeOwn() []Option {
	var own []Option
	for _, m := range catalog.Provider("anthropic") {
		if strings.HasPrefix(m.ID, "claude") {
			own = append(own, Option{Value: m.ID, Note: m.Name, Icon: "claude-color"})
		}
	}
	return own
}

// claudeViaMagpie is what magpie serves Claude Code, a model with a window
// of 1M or more marked [1m]: Claude Code takes any other for 200K, and
// compacts long before a 1M model needs it. It drops the mark before asking.
func claudeViaMagpie() []Option {
	big := map[string]bool{}
	for _, m := range magpieModels("claude") {
		big[m.ID] = m.Context >= 1_000_000
	}
	opts := viaMagpie("claude", "")
	for i, o := range opts {
		if big[o.Ref] {
			opts[i].Value += "[1m]"
		}
	}
	return opts
}

// claude1M marks [1m] a magpie model whose window is 1M or more, as
// magpie's list or models.dev gives it. Claude Code (2.1.284 and before)
// takes any Claude model not so marked for 200K, CLAUDE_CODE_MAX_CONTEXT_TOKENS
// or not, and compacts it, over and over, long before it runs out; a ref
// written in bare (typed, or picked while the window wasn't known) was left
// that way.
func claude1M() func(ref string) string {
	const mark = "[1m]"
	window := map[string]int{}
	for _, m := range magpieModels("claude") {
		window[m.ID] = m.Context
	}
	return func(ref string) string {
		if ref == "" || strings.HasSuffix(ref, mark) {
			return ref
		}
		if cmp.Or(window[ref], catalog.ContextOf(ref)) >= 1_000_000 {
			return ref + mark
		}
		return ref
	}
}

// claudeWindow is the context window to tell Claude Code for the models it
// runs on, 0 when magpie doesn't know it. One value serves every model not
// marked [1m]: the main model's, or when that one is marked, the smallest of
// the tiers' that aren't, so none of them outgrows its own.
func claudeWindow(main string, tiers map[string]string) int {
	const mark = "[1m]"
	window := map[string]int{}
	for _, m := range magpieModels("claude") {
		window[m.ID] = m.Context
	}
	if !strings.HasSuffix(main, mark) {
		return window[main]
	}
	w := 0
	for _, t := range claudeTiers {
		if v := tiers[t]; !strings.HasSuffix(v, mark) {
			if c := window[v]; c > 0 && (w == 0 || c < w) {
				w = c
			}
		}
	}
	return w
}

// claudeManaged is where an administrator's Claude Code settings live; a var
// so tests can point it elsewhere.
var claudeManaged = func() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode/managed-settings.json"
	case "windows":
		return `C:\ProgramData\ClaudeCode\managed-settings.json`
	}
	return "/etc/claude-code/managed-settings.json"
}

// StandIn is the model Claude Code is set to use in place of one it named
// that magpie doesn't serve: claude-haiku-4-5-… for a title or a small
// task goes to its haiku tier's model, and a name of no tier to its main
// model. For Codex, the model it is set to (codexStandIn). "" when the
// agent isn't routed through magpie or is neither. For gateway.StandIn.
func StandIn(agent, model string) string {
	if agent != "claude" && agent != "codex" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if agent == "codex" {
		return codexStandIn(filepath.Join(home, ".codex", "config.toml"))
	}
	if m := claudeStandIn(filepath.Join(home, ".claude", "settings.json"), model); m != "" || runtime.GOOS != "windows" {
		return m
	}
	// a Claude Code in WSL is known by the same User-Agent
	return wslClaudeStandIn(model)
}

func claudeStandIn(path, model string) string { return claudeStandInAt(path, model, gateway.URL()) }

// claudeStandInAt is claudeStandIn for a Claude Code that reaches the
// gateway at gw.
func claudeStandInAt(path, model, gw string) string {
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	if env("ANTHROPIC_BASE_URL") != gw {
		return ""
	}
	m := strings.ToLower(model)
	for _, t := range claudeTiers {
		if strings.Contains(m, t) {
			if v := env(tierEnv(t)); v != "" {
				return v
			}
			if t == "haiku" {
				if v := env("ANTHROPIC_SMALL_FAST_MODEL"); v != "" {
					return v
				}
			}
			break
		}
	}
	return env("ANTHROPIC_MODEL")
}
