package usage

import (
	"encoding/csv"
	"slices"
	"strings"
	"testing"
)

func TestLedgerCSVKeepsCreditsAndWebSocketTransport(t *testing.T) {
	credits := 0.7
	var out strings.Builder
	if err := WriteCSV(&out, []Row{{Record: Record{
		Credits: &credits, Transport: "websocket", UpstreamTransport: "websocket", RequestType: "ws_v2",
	}}}); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"credits": "0.7", "transport": "websocket", "upstream_transport": "websocket", "request_type": "ws_v2",
	} {
		at := slices.Index(rows[0], name)
		if at < 0 {
			t.Errorf("missing CSV column %s", name)
		} else if got := rows[1][at]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
