package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/provider"
)

func TestCodexSocketTraceNamesItsSeatAndAttempt(t *testing.T) {
	for _, auth := range []struct{ name, token, who string }{
		{"sign-in", "chatgpt-token", "Codex's own sign-in"},
		{"api-key", "sk-test-only", "Codex's API key"},
	} {
		for _, end := range []struct {
			name, event string
			status      int
		}{
			{"completed", `{"type":"response.completed","response":{"id":"trace-response","model":"gpt-6.1-sol","status":"completed","usage":{"input_tokens":12,"output_tokens":2}}}`, 200},
			{"failed", `{"type":"response.failed","response":{"id":"trace-response","status":"failed"}}`, 502},
			{"cancelled", `{"type":"response.cancelled","response":{"id":"trace-response","status":"cancelled"}}`, 499},
			{"disconnected", "", 502},
		} {
			t.Run(auth.name+"/"+end.name, func(t *testing.T) {
				fresh(t)
				release := make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
					c, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer c.CloseNow()
					ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
					defer cancel()
					if _, _, err := c.Read(ctx); err != nil {
						t.Error(err)
						return
					}
					if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"trace-response"}}`)); err != nil {
						t.Error(err)
						return
					}
					select {
					case <-release:
					case <-ctx.Done():
						return
					}
					if end.event != "" {
						if err := c.Write(ctx, websocket.MessageText, []byte(end.event)); err != nil {
							t.Error(err)
							return
						}
						_, _, _ = c.Read(ctx)
					}
				})
				wasAPI := codexAPIBase
				codexAPIBase = provider.CodexBase
				t.Cleanup(func() { codexAPIBase = wasAPI })
				s := New()
				finished := make(chan struct{})
				gw := httptest.NewServer(lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == CodexPath+"/responses" {
						defer close(finished)
					}
					s.Handler().ServeHTTP(w, r)
				})))
				defer gw.Close()
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				c, _, err := websocket.Dial(ctx, gw.URL+CodexPath+"/responses", &websocket.DialOptions{HTTPHeader: http.Header{
					"Authorization": {"Bearer " + auth.token}, "Session_id": {"trace-test"},
				}})
				if err != nil {
					t.Fatal(err)
				}
				defer c.CloseNow()
				body := `{"type":"response.create","model":"gpt-6.1-sol","reasoning":{"effort":"high"},"service_tier":"priority","input":[{"role":"user","content":"hello"}]}`
				if err := c.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
					t.Fatal(err)
				}
				if _, _, err := c.Read(ctx); err != nil {
					t.Fatal(err)
				}
				live := s.Trace(ctx, 0, 0).Routes
				if len(live) != 1 {
					t.Fatalf("live routes: %d", len(live))
				}
				route := live[0]
				if route.Transport != "websocket" || route.UpstreamTransport != "websocket" {
					t.Errorf("native WS transport missing from Routing: %q / %q", route.Transport, route.UpstreamTransport)
				}
				if len(route.Order) != 1 || route.Order[0].Who != auth.who || route.Order[0].Model != "gpt-6.1-sol" {
					t.Errorf("Routing must name the native account/key, got %+v", route.Order)
				}
				if len(route.Tries) != 1 || route.Tries[0].Done || route.Tries[0].Effort != "high" {
					t.Errorf("Routing must show the live attempt, got %+v", route.Tries)
				}
				res, err := http.Get(gw.URL + "/v1/magpie/route?session=trace-test")
				if err != nil {
					t.Fatal(err)
				}
				var session struct {
					Route SessionRoute `json:"route"`
				}
				err = json.NewDecoder(res.Body).Decode(&session)
				res.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if session.Route.Model != "openai/gpt-6.1-sol" || len(session.Route.Tries) != 1 {
					t.Errorf("session route lost the answering model: %+v", session.Route)
				}
				unblock()
				_, event, err := c.Read(ctx)
				if end.event != "" && (err != nil || !strings.Contains(string(event), `"response.`)) {
					t.Fatalf("terminal event: %s, %v", event, err)
				}
				c.CloseNow()
				select {
				case <-finished:
				case <-ctx.Done():
					t.Fatal("socket handler did not finish")
				}
				route = s.Trace(ctx, 0, 0).Routes[0]
				if !route.Done || route.Status != end.status || len(route.Tries) != 1 {
					t.Fatalf("final route missing the attempt: %+v", route)
				}
				attempt := route.Tries[0]
				if !attempt.Done || attempt.Status != end.status || attempt.Millis != route.Millis || attempt.Error != route.Error {
					t.Errorf("attempt did not finish with its route: %+v", attempt)
				}
			})
		}
	}
}
