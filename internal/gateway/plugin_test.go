package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// A plugin's provider signed in to is a provider like the others: each of
// its models is spoken to on the API its AI SDK package speaks, at the
// base URL the plugin's loader gives, and the plugin's fetch carries the
// request with the sign-in it keeps.
func TestPluginProvider(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)

	type seen struct {
		path, auth, model, body string
	}
	var mu sync.Mutex
	var got []seen
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("X-Plugin-Model"), string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case r.URL.Path == "/v1/chat/completions":
			fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi from chat"},"finish_reason":null}]}`+"\n\n")
			fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
		case r.URL.Path == "/v1/messages":
			for _, e := range []string{
				`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"fake-claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi from claude"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", e)
			}
		case strings.HasPrefix(r.URL.Path, "/v1/models/"):
			fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hi from gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2}}`+"\n\n")
		default:
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	t.Setenv("FAKE_BASE", up.URL+"/v1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Find("fakeco"); err == nil {
		t.Fatal("fakeco is a provider before it is signed in to")
	}
	if _, err := plugin.APIKey(ctx, "fakeco", 0, nil, "k1"); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("fakeco")
	if err != nil || !p.IsPlugin() || p.Name != "FakeCo" {
		t.Fatalf("Find = %+v, %v", p, err)
	}
	for model, want := range map[string]provider.Protocol{"fake-1": provider.Chat, "fake-claude": provider.Anthropic, "fake-gemini": provider.CodeAssist} {
		if apis := p.APIs(model); len(apis) != 1 || apis[0] != want {
			t.Fatalf("APIs(%s) = %v", model, apis)
		}
	}

	s := New()
	for _, c := range []struct{ model, text, path string }{
		{"fakeco/fake-1", "hi from chat", "/v1/chat/completions"},
		{"fakeco/fake-claude", "hi from claude", "/v1/messages"},
		{"fakeco/fake-gemini", "hi from gemini", "/v1/models/fake-gemini:streamGenerateContent?alt=sse"},
	} {
		code, body := postAs(t, s, "", `{"model":"`+c.model+`","messages":[{"role":"user","content":"hello"}]}`)
		if code != 200 || !strings.Contains(body, c.text) {
			t.Fatalf("%s: %d %s", c.model, code, body)
		}
		mu.Lock()
		last := got[len(got)-1]
		mu.Unlock()
		model := strings.TrimPrefix(c.model, "fakeco/")
		if last.path != c.path || last.auth != "Bearer k1" || last.model != model || !strings.Contains(last.body, "hello") {
			t.Fatalf("%s reached upstream as %+v", c.model, last)
		}
		if strings.Contains(last.body, `"request"`) {
			t.Fatalf("%s kept Code Assist's envelope: %s", c.model, last.body)
		}
	}

	if err := plugin.SignOut(ctx, "fakeco"); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Find("fakeco"); err == nil {
		t.Fatal("fakeco is still a provider after signing out")
	}
}
