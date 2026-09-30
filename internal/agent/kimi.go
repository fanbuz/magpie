package agent

// Kimi Code, Moonshot's kimi CLI, keeps its settings in ~/.kimi/config.toml
// ($KIMI_SHARE_DIR's): the model sessions start with as default_model, a key
// of its [models."<key>"] tables, each naming a [providers.<name>] table and
// the model to ask it for. magpie adds itself as the provider "magpie" (the
// gateway, spoken to as Kimi's own chat completions, so the thinking goes
// both ways as reasoning_content) and one model table per catalog model,
// keyed "magpie/<provider>/<model>", so the catalog joins Kimi's /model
// picker. The default the user had is stashed and put back when magpie steps
// out. Kimi refuses a config whose default_model or a model's provider is
// missing, so the provider goes in before the models and the default after,
// and out the other way round.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// kimiModelTable is the header prefix of every model table magpie writes.
var kimiModelTable = `models."` + magpieID + "/"

// kimiContext is what Kimi is told of a model whose context magpie doesn't
// know: it has to be told one, and compacts as it nears it.
const kimiContext = 128000

func kimiModelTables() []edit.Table {
	var out []edit.Table
	for _, m := range magpieModels("kimi") {
		ctx := m.Context
		if ctx <= 0 {
			ctx = kimiContext
		}
		kvs := []edit.KV{
			{Path: "provider", Value: magpieID},
			{Path: "model", Value: m.ID},
			{Path: "max_context_size", Value: ctx},
		}
		var caps []string
		if len(m.Efforts) > 0 {
			caps = append(caps, strconv.Quote("thinking"))
		}
		if m.Images {
			caps = append(caps, strconv.Quote("image_in"))
		}
		if len(caps) > 0 {
			kvs = append(kvs, edit.KV{Path: "capabilities", Value: edit.Raw("[" + strings.Join(caps, ", ") + "]")})
		}
		out = append(out, edit.Table{Name: "models." + strconv.Quote(magpieID+"/"+m.ID), KVs: kvs})
	}
	return out
}

func kimi(home string) *Agent {
	dir := os.Getenv("KIMI_SHARE_DIR")
	if dir == "" {
		dir = filepath.Join(home, ".kimi")
	}
	path := filepath.Join(dir, "config.toml")
	key := "kimi:" + path + ":default_model"
	get := func() string { v, _ := edit.GetTOMLTop(path, "default_model"); return v }
	providerTable := "providers." + magpieID
	writeMagpie := func() error {
		if err := edit.SetTOMLTable(path, providerTable,
			edit.KV{Path: "type", Value: "kimi"},
			edit.KV{Path: "base_url", Value: gatewayV1()},
			edit.KV{Path: "api_key", Value: gateway.Token},
		); err != nil {
			return err
		}
		return edit.SetTOMLTables(path, []string{kimiModelTable}, kimiModelTables())
	}
	dropMagpie := func() error {
		if err := edit.SetTOMLTables(path, []string{kimiModelTable}, nil); err != nil {
			return err
		}
		return edit.DelTOMLTable(path, providerTable)
	}
	// setDefault sets default_model, or takes it out for ""
	setDefault := func(v string) error {
		if v == "" {
			return edit.DelTOMLTop(path, "default_model")
		}
		return edit.SetTOMLTop(path, edit.KV{Path: "default_model", Value: v})
	}
	// ownModel reports whether Kimi has a model of the user's by this key
	ownModel := func(k string) bool {
		t, err := edit.GetTOMLTable(path, "models."+strconv.Quote(k))
		return err == nil && t != nil
	}
	return atomic(&Agent{
		ID: "kimi", Name: "Kimi Code", Icon: "kimi", Aliases: []string{"kimi-code", "kimi-cli"},
		UA:  []string{"kimicli"},
		Bin: "kimi", Dir: dir, Path: path,
		Sync: func() error {
			t, err := edit.GetTOMLTable(path, providerTable)
			if err != nil || t == nil {
				return err
			}
			return writeMagpie()
		},
		Notice: func() string {
			if Running(`(^|/)kimi( |$)`, `(^|/)kimi-cli( |$)`) {
				return "Kimi Code reads its settings at start-up — restart open kimi sessions to use this."
			}
			return ""
		},
		Check: func() string {
			v := get()
			if !usesMagpie(v) {
				return ""
			}
			m, err := edit.GetTOMLTable(path, "models."+strconv.Quote(v))
			if err != nil {
				return err.Error()
			}
			if m == nil {
				return "Kimi Code's [models." + strconv.Quote(v) + "] (config.toml) is gone, so it no longer reaches magpie"
			}
			t, err := edit.GetTOMLTable(path, providerTable)
			if err != nil {
				return err.Error()
			}
			if t == nil {
				return "Kimi Code's [" + providerTable + "] (config.toml) is gone, so it no longer reaches magpie"
			}
			return wiringOff("Kimi Code", path, func(k string) (string, bool) { v, ok := t[k]; return v, ok },
				"base_url", gatewayV1(), "api_key", gateway.Token)
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				cur := get()
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !usesMagpie(cur) {
						stash(map[string]string{key: cur})
					}
					if err := writeMagpie(); err != nil {
						return err
					}
					return setDefault(v)
				}
				if v == "" && usesMagpie(cur) {
					// back to the default the user had, if Kimi still has it
					if was := unstash(key); was != "" && !usesMagpie(was) && ownModel(was) {
						v = was
					}
				}
				if err := setDefault(v); err != nil {
					return err
				}
				return dropMagpie()
			},
			Options: func(cur map[string]string) []Option {
				return append(kimiOwnOptions(path, cur["model"]), viaMagpie("kimi", magpieID+"/")...)
			},
		}},
	}, path)
}

// kimiOwnOptions are the models of the user's own in Kimi's config: its
// Kimi Code sign-in's and any provider they added, and the current one.
func kimiOwnOptions(path, cur string) []Option {
	tables, _ := edit.TOMLTables(path)
	seen := map[string]bool{}
	var out []Option
	for _, t := range tables {
		k, ok := strings.CutPrefix(t, "models.")
		if !ok {
			continue
		}
		if u, err := strconv.Unquote(k); err == nil {
			k = u
		} else if strings.Contains(k, ".") {
			continue // a table under a model's, not one
		}
		if seen[k] || strings.HasPrefix(k, magpieID+"/") {
			continue
		}
		seen[k] = true
		m, _ := edit.GetTOMLTable(path, t)
		icon := modelIcon("", m["model"])
		if icon == "" && strings.Contains(m["provider"], "kimi") {
			icon = "kimi"
		}
		out = append(out, Option{Value: k, Icon: icon})
	}
	if cur != "" && !seen[cur] && !strings.HasPrefix(cur, magpieID+"/") {
		out = append([]Option{{Value: cur, Icon: modelIcon("", cur)}}, out...)
	}
	return group("Kimi Code", out)
}
