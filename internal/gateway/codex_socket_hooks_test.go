package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

func TestCodexSocketKeepsAsyncHooksOnHTTP(t *testing.T) {
	fresh(t)
	t.Cleanup(plugin.Settle)
	r := httptest.NewRequest(http.MethodGet, CodexPath+"/responses", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Upgrade", "websocket")
	if !codexSocketPolicy(r) {
		t.Fatal("native socket refused without hooks")
	}
	hook := filepath.Join(t.TempDir(), "context.hooks.js")
	if err := os.WriteFile(hook, []byte(`export function onRequest(body) { return body }`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(t.Context(), hook); err != nil {
		t.Fatal(err)
	}
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("WS bypassed the enabled request hook")
		w.WriteHeader(http.StatusTeapot)
	})
	w := httptest.NewRecorder()
	lanGuard(New().Handler()).ServeHTTP(w, r)
	if w.Code != http.StatusUpgradeRequired {
		t.Fatalf("enabled async hook needs HTTP fallback, got %d: %s", w.Code, w.Body.String())
	}
	if err := plugin.SetOff(hook, true); err != nil {
		t.Fatal(err)
	}
	if !codexSocketPolicy(r) {
		t.Fatal("disabled hook still refuses native sockets")
	}
}
