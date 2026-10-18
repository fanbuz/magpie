package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/provider"
)

// A known ChatGPT compaction needs HTTP's writer/account checks, even
// with one account on. Unknown seals still belong to the native backend.
func TestCodexSocketKeepsRecordedCompactionOnHTTP(t *testing.T) {
	for _, afterTurn := range []bool{false, true} {
		t.Run(map[bool]string{false: "first turn", true: "later turn"}[afterTurn], func(t *testing.T) {
			codexSignedIn(t)
			wroteCheckpoint("recorded-seal", candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{User: "gone@example.com"}}, model: "gpt-5.5"})
			var received atomic.Int32
			upDone := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(upDone)
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer c.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				for {
					_, body, err := c.Read(ctx)
					if err != nil {
						return
					}
					received.Add(1)
					if strings.Contains(string(body), "recorded-seal") {
						t.Error("a recorded compaction bypassed HTTP's writer check")
						_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"error","error":{"code":"upstream_saw_recorded"}}`))
						continue
					}
					if !strings.Contains(string(body), "unknown-seal") {
						t.Error("the unknown native compaction was changed")
					}
					_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"first","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`))
				}
			}))
			defer up.Close()
			was := provider.CodexBase
			provider.CodexBase = up.URL + "/backend-api/codex"
			t.Cleanup(func() { provider.CodexBase = was })
			s := New()
			gwDone := make(chan struct{})
			gw := httptest.NewServer(lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(gwDone)
				s.Handler().ServeHTTP(w, r)
			})))
			defer gw.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, gw.URL+CodexPath+"/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-token"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			send := func(seal string) string {
				t.Helper()
				body := `{"type":"response.create","model":"gpt-5.5","input":[{"type":"compaction","encrypted_content":"` + seal + `"}]}`
				if err := c.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
					t.Fatal(err)
				}
				_, reply, err := c.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return string(reply)
			}
			if afterTurn && !strings.Contains(send("unknown-seal"), "response.completed") {
				t.Fatal("unknown native compaction was not relayed")
			}
			if reply := send("recorded-seal"); !strings.Contains(reply, "magpie_http_required") {
				t.Errorf("recorded compaction must use HTTP routing, got %s", reply)
			}
			c.CloseNow()
			select {
			case <-gwDone:
			case <-ctx.Done():
				t.Fatal("gateway socket handler did not stop")
			}
			select {
			case <-upDone:
			case <-ctx.Done():
				t.Fatal("upstream socket did not stop")
			}
			want := int32(0)
			if afterTurn {
				want = 1
			}
			if got := received.Load(); got != want {
				t.Errorf("forwarded %d frames, want %d", got, want)
			}
		})
	}
}
