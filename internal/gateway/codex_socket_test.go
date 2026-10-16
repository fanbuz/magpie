package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/usage"
)

func TestCodexSocketContinuesAndCountsEachResponse(t *testing.T) {
	fresh(t)
	done := make(chan struct{})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		if r.Header.Get("Authorization") != "Bearer chatgpt-token" || r.Header.Get("chatgpt-account-id") != "acct-1" {
			t.Error("sign-in headers lost")
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		for i := 1; i <= 2; i++ {
			_, b, err := c.Read(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			if i == 2 && !strings.Contains(string(b), `"previous_response_id":"r1"`) {
				t.Error("continuation changed")
			}
			events := []string{fmt.Sprintf(`{"type":"response.created","response":{"id":"r%d"}}`, i), `{"type":"response.output_text.delta","delta":"OK"}`, fmt.Sprintf(`{"type":"response.completed","response":{"id":"r%d","model":"gpt-6-astra","status":"completed","usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":5},"output_tokens":2}}}`, i)}
			for _, e := range events {
				if err := c.Write(ctx, websocket.MessageText, []byte(e)); err != nil {
					t.Error(err)
					return
				}
			}
		}
		_, _, _ = c.Read(ctx)
	})
	s := New()
	gw := httptest.NewServer(lanGuard(s.Handler()))
	defer gw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, res, err := websocket.Dial(ctx, gw.URL+CodexPath+"/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer chatgpt-token"}, "Chatgpt-Account-Id": {"acct-1"}, "Session_id": {"ws-test"}}})
	if err != nil {
		t.Fatalf("upgrade: %v (%v)", err, res)
	}
	defer c.CloseNow()
	for i := 1; i <= 2; i++ {
		b := `{"type":"response.create","model":"gpt-6-astra","input":[{"role":"user","content":"Say OK"}]}`
		if i == 2 {
			b = `{"type":"response.create","model":"gpt-6-astra","previous_response_id":"r1","input":[{"role":"user","content":"Again"}]}`
		}
		if err := c.Write(ctx, websocket.MessageText, []byte(b)); err != nil {
			t.Fatal(err)
		}
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), `"response.completed"`) {
				break
			}
		}
	}
	c.CloseNow()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("upstream reader did not stop")
	}
	rows := usage.Load(time.Time{})
	if len(rows) != 2 {
		t.Fatalf("got %d usage rows, want two", len(rows))
	}
	for i, r := range rows {
		if r.Transport != "websocket" || r.UpstreamTransport != "websocket" || r.Input != 7 || r.CacheRead != 5 || r.Output != 2 || r.ResponseID != fmt.Sprintf("r%d", i+1) || r.Status != 200 {
			t.Errorf("wrong usage: %+v", r)
		}
	}
	if s.Busy().Requests != 0 {
		t.Fatal("finished responses still busy")
	}
}

func TestCodexSocketDisconnectCountsUnfinishedTurn(t *testing.T) {
	fresh(t)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if _, _, err = c.Read(ctx); err != nil {
			t.Error(err)
			return
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"cut"}}`))
	})
	s := New()
	done := make(chan struct{})
	gw := httptest.NewServer(lanGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(done); s.Handler().ServeHTTP(w, r) })))
	defer gw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, gw.URL+CodexPath+"/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer chatgpt-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-6-astra","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := c.Read(ctx); err != nil {
			break
		}
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("handler did not stop")
	}
	rows := usage.Load(time.Time{})
	if len(rows) != 1 || rows[0].Status != 502 || rows[0].Transport != "websocket" {
		t.Fatalf("unfinished turn: %+v", rows)
	}
	if s.Busy().Requests != 0 {
		t.Fatal("unfinished response kept a slot")
	}
}

func TestCodexSocketMissingContextIsNotReplayed(t *testing.T) {
	fresh(t)
	const input = `{"type":"response.create","model":"gpt-6-astra","previous_response_id":"lost","input":[{"role":"user","content":"increment only"}]}`
	const failure = `{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"Restore the complete context"}}`
	done := make(chan struct{})
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		if string(b) != input {
			t.Errorf("increment was rewritten: %s", b)
		}
		if err := c.Write(ctx, websocket.MessageText, []byte(failure)); err != nil {
			t.Error(err)
			return
		}
		if _, b, err := c.Read(ctx); err == nil {
			t.Errorf("unexpected automatic replay: %s", b)
		}
	})
	s := New()
	gw := httptest.NewServer(lanGuard(s.Handler()))
	defer gw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, gw.URL+CodexPath+"/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer chatgpt-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageText, []byte(input)); err != nil {
		t.Fatal(err)
	}
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != failure {
		t.Fatalf("recovery error changed: %s", b)
	}
	c.CloseNow()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("upstream reader did not stop")
	}
	rows := usage.Load(time.Time{})
	if len(rows) != 1 || rows[0].Status != 502 {
		t.Fatalf("failed response usage: %+v", rows)
	}
}
