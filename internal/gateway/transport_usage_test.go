package gateway

import (
	"github.com/yetone/magpie/internal/provider"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

func TestUsageTransportObservesResponse(t *testing.T) {
	for _, tc := range []struct{ content, want string }{{"text/event-stream; charset=utf-8", "sse"}, {"application/json", "http"}, {"", "http"}} {
		t.Run(tc.want+tc.content, func(t *testing.T) {
			fresh(t)
			req := httptest.NewRequest("POST", "/v1/responses", nil)
			w, r := observeTransport(httptest.NewRecorder(), req)
			w.Header().Set("Content-Type", tc.content)
			w.WriteHeader(http.StatusOK)
			appendUsage(r, usage.Record{Time: time.Now(), Model: "test", Status: 200})
			rows := usage.Load(time.Time{})
			if len(rows) != 1 || rows[0].Transport != tc.want {
				t.Fatalf("transport: %+v", rows)
			}
		})
	}
}

func TestRequestTypeUsesExplicitClientFlag(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"stream":true}`, "stream"}, {`{"stream":false}`, "sync"},
		{`{}`, ""}, {`{"stream":null}`, ""}, {`{"stream":"true"}`, ""}, {`{`, ""},
	} {
		t.Run(tc.body, func(t *testing.T) {
			fresh(t)
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(tc.body))
			w, r := observeTransport(httptest.NewRecorder(), req)
			body, ok := New().requestBody(w, r, provider.Responses)
			if !ok || string(body) != tc.body {
				t.Fatal("body changed")
			}
			// The error response's HTTP type must not erase the requested stream mode.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			appendUsage(r, usage.Record{Time: time.Now(), Model: "test", Status: 400})
			rows := usage.Load(time.Time{})
			if len(rows) != 1 || rows[0].RequestType != tc.want || rows[0].Transport != "http" {
				t.Fatalf("request type: %+v", rows)
			}
		})
	}
}
