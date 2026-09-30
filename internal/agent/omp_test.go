package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

func TestOmp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, k := range []string{"PI_CODING_AGENT_DIR", "PI_CONFIG_DIR", "OMP_PROFILE", "PI_PROFILE"} {
		t.Setenv(k, "")
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".omp", "agent")
	configPath, modelsPath := filepath.Join(dir, "config.yml"), filepath.Join(dir, "models.yml")
	read := func(p string) (map[string]any, string) {
		var m map[string]any
		b, _ := os.ReadFile(p)
		yaml.Unmarshal(b, &m)
		return m, string(b)
	}
	roles := func(c map[string]any) map[string]any { r, _ := c["modelRoles"].(map[string]any); return r }
	providers := func(m map[string]any) map[string]any { p, _ := m["providers"].(map[string]any); return p }
	os.MkdirAll(dir, 0o755)
	os.WriteFile(configPath, []byte("# mine\ntheme: dark\nmodelRoles:\n  default: anthropic/claude-opus-5 # main\n  smol: openai/gpt-6-mini\n"), 0o644)
	// an older omp's models.json, not yet moved to models.yml
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers":{"mine":{"baseUrl":"https://x","apiKey":"X","api":"openai-completions","models":[{"id":"a"}]}}}`), 0o644)

	f := omp(home).Field("model")
	if f.Get() != "anthropic/claude-opus-5" {
		t.Fatalf("get: %q", f.Get())
	}
	if err := f.Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	c, raw := read(configPath)
	if roles(c)["default"] != "magpie/deepseek/pro" || roles(c)["smol"] != "openai/gpt-6-mini" || c["theme"] != "dark" ||
		!strings.Contains(raw, "# mine") || !strings.Contains(raw, "# main") {
		t.Fatalf("config:\n%s", raw)
	}
	m, raw := read(modelsPath)
	ps := providers(m)
	mp, ok := ps["magpie"].(map[string]any)
	if !ok || ps["mine"] == nil || mp["auth"] != "none" || mp["api"] != "openai-completions" || !strings.HasSuffix(mp["baseUrl"].(string), "/v1") {
		t.Fatalf("models:\n%s", raw)
	}
	ids := map[string]bool{}
	for _, x := range mp["models"].([]any) {
		ids[x.(map[string]any)["id"].(string)] = true
	}
	if !ids["deepseek/pro"] || !ids["deepseek/flash"] {
		t.Fatalf("models: %v", ids)
	}

	// its own model: magpie steps out
	if err := f.Set("moonshotai/kimi-k3"); err != nil {
		t.Fatal(err)
	}
	c, _ = read(configPath)
	m, _ = read(modelsPath)
	if roles(c)["default"] != "moonshotai/kimi-k3" || providers(m)["magpie"] != nil || providers(m)["mine"] == nil {
		t.Fatalf("own: %v %v", c, m)
	}

	// another role through magpie keeps it in models.yml
	f.Set("magpie/deepseek/flash")
	os.WriteFile(configPath, []byte("modelRoles:\n  default: magpie/deepseek/flash\n  smol: magpie/deepseek/pro\n"), 0o644)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, _ = read(configPath)
	m, _ = read(modelsPath)
	if roles(c)["default"] != nil || roles(c)["smol"] != "magpie/deepseek/pro" || providers(m)["magpie"] == nil {
		t.Fatalf("reset with smol: %v %v", c, m)
	}

	// reset: only default goes, and magpie with it
	os.WriteFile(configPath, []byte("theme: dark\nmodelRoles:\n  default: magpie/deepseek/flash\n  smol: openai/gpt-6-mini\n"), 0o644)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	c, _ = read(configPath)
	m, _ = read(modelsPath)
	if roles(c)["default"] != nil || roles(c)["smol"] != "openai/gpt-6-mini" || c["theme"] != "dark" || providers(m)["magpie"] != nil || providers(m)["mine"] == nil {
		t.Fatalf("reset: %v %v", c, m)
	}
}
