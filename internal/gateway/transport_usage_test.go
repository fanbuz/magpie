package gateway

import (
	"net/http"
	"net/http/httptest"
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
