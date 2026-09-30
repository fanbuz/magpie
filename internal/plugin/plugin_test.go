package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sandbox gives the test its own magpie folders and the Bun on PATH; a
// machine without Bun skips it.
func sandbox(t *testing.T) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(Settle)
}

func TestFakePlugin(t *testing.T) {
	sandbox(t)
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s %s\n\n", r.URL.Path, b)
		fl.Flush()
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("FAKE_BASE", srv.URL+"/v1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	ls, err := Plugins(ctx)
	if err != nil || len(ls) != 1 || ls[0].Error != "" {
		t.Fatalf("Plugins = %+v, %v", ls, err)
	}
	ps, err := Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].ID != "fakeco" || ps[0].Name != "FakeCo" || ps[0].SignedIn || len(ps[0].Methods) != 2 {
		t.Fatalf("Providers = %+v", ps)
	}
	npm := map[string]string{}
	for _, m := range ps[0].Models {
		npm[m.ID] = m.NPM
	}
	if npm["fake-1"] != "@ai-sdk/openai-compatible" || npm["fake-claude"] != "@ai-sdk/anthropic" || npm["fake-gemini"] != "@ai-sdk/google" {
		t.Fatalf("models = %+v", ps[0].Models)
	}

	// the browser method asks where, then a team only for work
	p, err := NextPrompt(ctx, "fakeco", 1, map[string]string{})
	if err != nil || p == nil || p.Key != "where" || len(p.Options) != 2 {
		t.Fatalf("first prompt = %+v, %v", p, err)
	}
	if p, _ := NextPrompt(ctx, "fakeco", 1, map[string]string{"where": "home"}); p != nil {
		t.Fatalf("home asked %+v", p)
	}
	if p, _ := NextPrompt(ctx, "fakeco", 1, map[string]string{"where": "work"}); p == nil || p.Key != "team" {
		t.Fatalf("work asked %+v", p)
	}
	if e, _ := Validate(ctx, "fakeco", 1, "team", ""); e != "Required" {
		t.Fatalf("Validate = %q", e)
	}
	in := map[string]string{"where": "work", "team": "blue"}
	a, err := Authorize(ctx, "fakeco", 1, in)
	if err != nil || a.Method != "code" || !strings.Contains(a.URL, "where=work") {
		t.Fatalf("Authorize = %+v, %v", a, err)
	}
	if _, err := Finish(ctx, a.Session, "bad"); err != ErrFailed {
		t.Fatalf("a bad code: %v", err)
	}
	a, _ = Authorize(ctx, "fakeco", 1, in)
	if got, err := Finish(ctx, a.Session, "good"); err != nil || got != "fakeco" {
		t.Fatalf("Finish = %q, %v", got, err)
	}
	var saved map[string]map[string]any
	b, _ := os.ReadFile(AuthPath())
	json.Unmarshal(b, &saved)
	if saved["fakeco"]["type"] != "oauth" || saved["fakeco"]["refresh"] != "r-blue" || saved["fakeco"]["accountId"] != "me@fake" {
		t.Fatalf("saved %v", saved)
	}
	if fi, _ := os.Stat(AuthPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("plugin-auth.json is %v", fi.Mode().Perm())
	}
	if !SignedIn("fakeco") {
		t.Fatal("not signed in")
	}

	o, err := LoaderOptions(ctx, "fakeco")
	if err != nil || o.BaseURL != srv.URL+"/v1" || !o.Fetch || o.APIKey != "dummy" {
		t.Fatalf("LoaderOptions = %+v, %v", o, err)
	}
	res, err := Fetch(ctx, FetchRequest{
		Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
		URL: o.BaseURL + "/chat/completions", Method: "POST",
		Headers: map[string]string{"content-type": "application/json"},
		Body:    []byte(`{"model":"fake-1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Fetch = %d %v %v", res.StatusCode, res.Header, err)
	}
	if want := "data: /v1/chat/completions {\"model\":\"fake-1\"}\n\ndata: [DONE]\n\n"; string(body) != want {
		t.Fatalf("body = %q", body)
	}
	h := seen[len(seen)-1]
	// the plugin refreshed the stale token and saved it; the chat.headers
	// hook added its header; the plugin saw the provider's models
	if h.Get("Authorization") != "Bearer fresh" || h.Get("X-Plugin-Model") != "fake-1" || h.Get("X-Models") != "fake-1,fake-claude,fake-gemini" {
		t.Fatalf("headers = %v", h)
	}
	b, _ = os.ReadFile(AuthPath())
	json.Unmarshal(b, &saved)
	if saved["fakeco"]["access"] != "fresh" {
		t.Fatalf("refresh not saved: %v", saved)
	}

	// a request that is abandoned is aborted, and the host still answers
	cctx, ccancel := context.WithCancel(ctx)
	res, err = Fetch(cctx, FetchRequest{Provider: "fakeco", Model: "fake-1", URL: o.BaseURL + "/chat/completions", Method: "POST", Body: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	ccancel()
	io.ReadAll(res.Body)
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}

	if err := SignOut(ctx, "fakeco"); err != nil || SignedIn("fakeco") {
		t.Fatalf("SignOut: %v", err)
	}
	if err := SetOff(abs, true); err != nil {
		t.Fatal(err)
	}
	if ps := Cached(); len(ps) != 0 {
		t.Fatalf("a plugin turned off still lists %+v", ps)
	}
}
