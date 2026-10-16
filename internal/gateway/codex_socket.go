package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// codexSocketPolicy keeps features that need per-turn routing on HTTP. A
// native socket is pinned to Codex's own sign-in; it must never spend that
// sign-in past a pool, cap, managed key, or a request/reply transformation.
// Check again before each create, since settings may change on a long socket.
func codexSocketPolicy(r *http.Request) bool {
	cfg, err := settings.LoadChecked()
	if err != nil {
		return false
	}
	if redactionWanted() || middlewareWanted() || cfg.GatewayConversations || cfg.CodexTitles != "" || cfg.CodexDescriptions != "" || cfg.CodexPredictions != "" || cfg.CodexSubagentModel != "" {
		return false
	}
	if access.Caller(r.Context()).KeyID != "" || r.Header.Get(AccountHeader) != "" {
		return false
	}
	if _, held := keyHolds(r); held {
		return false
	}
	if _, held := accountHolds(r); held {
		return false
	}
	for _, p := range provider.All() {
		if p.ID != "codex" {
			continue
		}
		if p.Off || p.OwnPaused() || len(p.AlsoOn()) > 0 || p.MaxConcurrency != nil || p.MaxRPM != 0 || len(p.AccountConcurrency) > 0 || len(p.AccountCaps) > 0 || len(p.AccountWindowCaps) > 0 {
			return false
		}
		if p.Account != nil && provider.HoldCaps(p, "codex", p.Account.User).Holds() {
			return false
		}
	}
	return true
}

func (s *Server) codexSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != CodexPath+"/responses" || !codexSocketPolicy(r) {
		http.Error(w, "This Codex route requires HTTP; retry with POST and SSE", http.StatusUpgradeRequired)
		return
	}
	provider.SawCodexClient(r.Header)
	base := provider.CodexBase
	if apiKey(r.Header) {
		base = codexAPIBase
	}
	addr := base + "/responses"
	if r.URL.RawQuery != "" {
		addr += "?" + r.URL.RawQuery
	}
	headers := http.Header{}
	copyHeaders(headers, r.Header)
	for k := range headers {
		if strings.HasPrefix(strings.ToLower(k), "sec-websocket-") {
			headers.Del(k)
		}
	}
	headers.Del("Origin")
	headers.Del("Accept-Encoding")
	ctx, cancel := context.WithCancel(provider.ViaSignedIn(r.Context(), "codex"))
	defer cancel()
	// A failed upgrade is still an HTTP response. Do not accept the client
	// until the upstream accepts, so Codex can fall back without losing input.
	client := *s.client
	client.Timeout = 15 * time.Second
	upstream, res, err := websocket.Dial(ctx, addr, &websocket.DialOptions{HTTPClient: &client, HTTPHeader: headers})
	if err != nil {
		code := http.StatusBadGateway
		if res != nil {
			if res.StatusCode >= 400 {
				code = res.StatusCode
			}
			if res.Body != nil {
				res.Body.Close()
			}
			if code == 404 || code == 405 || code == 426 {
				code = http.StatusUpgradeRequired
			}
		}
		http.Error(w, "OpenAI WebSocket handshake failed", code)
		return
	}
	defer upstream.CloseNow()
	withGatewaySession(w, r)
	// Origin and caller permissions have already passed the gateway's outer
	// guards. Keep the WebSocket library's same-origin check as well.
	downstream, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer downstream.CloseNow()
	upstream.SetReadLimit(defaultBodyLimit)
	downstream.SetReadLimit(defaultBodyLimit)
	s.relayCodexSocket(ctx, r, downstream, upstream, base, requestID(res.Header))
}

type codexSocketEvent struct {
	fromClient bool
	kind       websocket.MessageType
	data       []byte
	err        error
}
type codexSocketTurn struct {
	start                   time.Time
	model, tier, stream, id string
	sniff                   *usageSniffer
	first                   firstToken
	route                   *Route
}

// relayCodexSocket owns turn state on one goroutine. Readers only deliver
// frames; they finish before the handler returns. No lock is held over I/O.
func (s *Server) relayCodexSocket(parent context.Context, r *http.Request, down, up *websocket.Conn, base, rid string) {
	ctx, cancel := context.WithCancel(parent)
	events := make(chan codexSocketEvent, 2)
	var readers sync.WaitGroup
	read := func(c *websocket.Conn, client bool) {
		defer readers.Done()
		for {
			k, b, err := c.Read(ctx)
			select {
			case events <- codexSocketEvent{client, k, b, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}
	readers.Add(2)
	go read(down, true)
	go read(up, false)
	turns := []*codexSocketTurn{}
	terminalStatus, terminalError := 499, "Client closed WebSocket before the response ended"
	defer func() {
		cancel()
		down.CloseNow()
		up.CloseNow()
		readers.Wait()
		for _, t := range turns {
			s.finishCodexSocketTurn(r, t, base, rid, terminalStatus, terminalError)
		}
	}()
	write := func(c *websocket.Conn, k websocket.MessageType, b []byte) error {
		wc, done := context.WithTimeout(ctx, 30*time.Second)
		defer done()
		return c.Write(wc, k, b)
	}
	for {
		var e codexSocketEvent
		select {
		case e = <-events:
		case <-ctx.Done():
			return
		}
		if e.err != nil {
			if !e.fromClient {
				terminalStatus, terminalError = 502, "Upstream closed WebSocket before the response ended"
			}
			return
		}
		if e.kind != websocket.MessageText {
			return
		}
		var v struct {
			Type       string `json:"type"`
			Model      string `json:"model"`
			Stream     string `json:"stream_id"`
			ResponseID string `json:"response_id"`
			Response   struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if json.Unmarshal(e.data, &v) != nil {
			return
		}
		if e.fromClient {
			if v.Type == "response.create" {
				_, routed := codexAccounts(r, v.Model)
				_, off := codexNativeOff(v.Model)
				if !codexSocketPolicy(r) || v.Model == "" || strings.Contains(v.Model, "/") || routed || off || len(turns) >= 32 {
					_ = write(down, websocket.MessageText, []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"magpie_http_required","message":"This request requires Magpie HTTP routing; reconnect using HTTP/SSE."}}`))
					return
				}
				now := time.Now()
				tr := s.trace.begin(Route{Time: now, Agent: agentOf(r), Session: sessionOf(r.Header), Model: v.Model, Provider: "openai"})
				t := &codexSocketTurn{start: now, model: v.Model, tier: bodyTier(provider.Responses, e.data), stream: v.Stream, sniff: newSniffer(provider.Responses, "text/event-stream"), first: firstToken{start: now}, route: tr}
				turns = append(turns, t)
				s.inFlight.Add(1)
			} else if v.Type != "response.cancel" {
				_ = write(down, websocket.MessageText, []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"magpie_http_required","message":"This WebSocket event is not supported by Magpie's native relay."}}`))
				return
			}
			if write(up, e.kind, e.data) != nil {
				return
			}
			continue
		}
		id := v.Response.ID
		if id == "" {
			id = v.ResponseID
		}
		at := -1
		for i, t := range turns {
			if id != "" && t.id == id {
				at = i
				break
			}
		}
		if at < 0 {
			for i, t := range turns {
				if t.stream == v.Stream && (id == "" || t.id == "" && v.Type == "response.created") {
					at = i
					break
				}
			}
		}
		if at >= 0 {
			t := turns[at]
			if id != "" {
				t.id = id
			}
			frame := append(append([]byte("data: "), e.data...), '\n', '\n')
			t.sniff.write(frame)
			t.first.see(frame)
			switch v.Type {
			case "response.completed", "response.failed", "response.incomplete", "response.cancelled", "error":
				status, msg := 200, ""
				if v.Type == "error" || v.Type == "response.failed" {
					status = 502
					msg = "OpenAI WebSocket response failed"
				}
				if v.Type == "response.cancelled" {
					status = 499
					msg = "OpenAI WebSocket response cancelled"
				}
				s.finishCodexSocketTurn(r, t, base, rid, status, msg)
				turns = append(turns[:at], turns[at+1:]...)
			}
		}
		if err := write(down, e.kind, e.data); err != nil && !errors.Is(err, context.Canceled) {
			return
		}
	}
}

func (s *Server) finishCodexSocketTurn(r *http.Request, t *codexSocketTurn, base, rid string, status int, msg string) {
	defer s.inFlight.Add(-1)
	s.lastDone.Store(time.Now().UnixNano())
	u := t.sniff.usage()
	ms := time.Since(t.start).Milliseconds()
	ttft, text := t.first.ms()
	p, account := codexServed(base, r.Header, nil)
	rec := usage.Record{Transport: "websocket", UpstreamTransport: "websocket", RouteID: t.route.ID, Time: t.start, Agent: agentOf(r), Provider: p, ProviderAccount: account, Host: provider.HostOf(base), Model: t.model, Requested: t.model, Served: u.Served, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Reasoning: u.Reasoning, TierAsked: t.tier, TierSent: t.tier, TierServed: u.Tier, Millis: ms, TTFT: ttft, FirstText: text, Flow: t.first.flowFor(u.Reasoning), Status: status, Error: msg, RequestID: rid, ResponseID: t.id, Endpoint: r.URL.Path, Session: sessionOf(r.Header), NativeSession: nativeSessionOf(r.Header), Stop: u.Stop}
	s.trace.update(t.route, func(tr *Route) {
		tr.Done = true
		tr.Status = status
		tr.Error = msg
		tr.Millis = ms
		tr.TTFT = ttft
		tr.FirstText = text
		tr.Tokens = u.Input + u.Output + u.CacheRead + u.CacheWrite
		tr.Output = u.Output
		tr.Usage = routeUsage(p, t.model, u)
	})
	appendUsage(r, rec)
}
