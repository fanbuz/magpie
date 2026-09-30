package gui

// Run with MAGPIE_USAGE_SCALE=1 go test -tags nogui ./internal/gui
// -run TestUsageScale -count=1 -v. The fixture is entirely synthetic.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

func TestUsageScale(t *testing.T) {
	if os.Getenv("MAGPIE_USAGE_SCALE") != "1" {
		t.Skip("large synthetic fixture; opt in with MAGPIE_USAGE_SCALE=1")
	}
	home := sandboxHome(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("OPENAI_BASE_URL", "")
	sessions.Reset()
	catalog.Reset()
	t.Cleanup(func() { sessions.Reset(); catalog.Reset() })
	now := time.Now().Add(-time.Hour)
	for i := 0; i < 2000; i++ {
		at := now.AddDate(0, 0, -(i % 90))
		sid := fmt.Sprintf("scale-%04d", i)
		path := filepath.Join(sessions.ClaudeDir(), "projects", "scale", sid+".jsonl")
		if i%4 == 1 {
			path = filepath.Join(sessions.DesktopDataDirs()[0], "local-agent-mode-sessions", "account", "org", "local_"+sid, ".claude", "projects", "scale", sid+".jsonl")
		}
		if i%4 == 3 {
			path = filepath.Join(sessions.CodexDir(), "sessions", "2026", "09", "30", "rollout-2026-09-30T00-00-00-"+sid+".jsonl")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w := bufio.NewWriter(f)
		if i%4 == 3 {
			fmt.Fprintf(w, `{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"cwd":"/work/scale"}}`+"\n", at.Format(time.RFC3339), sid)
			fmt.Fprintln(w, `{"type":"turn_context","payload":{"model":"gpt-6.1-sol","effort":"high"}}`)
		} else {
			fmt.Fprintf(w, `{"type":"user","timestamp":%q,"message":{"role":"user","content":"Synthetic scale fixture"},"sessionId":%q}`+"\n", at.Format(time.RFC3339), sid)
		}
		for j := 0; j < 150; j++ {
			stamp := at.Add(time.Duration(j+1) * time.Second).Format(time.RFC3339)
			if i%4 == 3 {
				fmt.Fprintf(w, `{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10}}}}`+"\n", stamp, (j+1)*100, (j+1)*20, (j+1)*10)
			} else {
				fmt.Fprintf(w, `{"type":"assistant","timestamp":%q,"sessionId":%q,"requestId":"r-%d-%d","message":{"id":"m-%d-%d","model":"claude-sonnet-5","content":[{"type":"text","text":"Synthetic reply"}],"usage":{"input_tokens":80,"cache_read_input_tokens":20,"output_tokens":10}}}`+"\n", stamp, sid, i, j, i, j)
			}
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at.Add(3*time.Minute), at.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(usage.Path()), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(usage.Path())
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for i := 0; i < 50_000; i++ {
		if err := enc.Encode(usage.Record{Time: now.AddDate(0, 0, -(i % 90)), Agent: "opencode", Provider: "relay", Model: "gpt-6.1-sol", Input: 80, Output: 10, Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if n := len(sessions.List(2000)); n != 2000 {
		t.Fatalf("sessions: %d", n)
	}
	if n := len(sessions.Calls(time.Time{})); n != 300_000 {
		t.Fatalf("calls: %d", n)
	}
	sessions.Saved()
	info, err := os.Stat(sessions.CachePath())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("summary index: %.2f MiB", float64(info.Size())/(1<<20))
	sessions.Reset()
	runtime.GC()
	start := time.Now()
	_ = sessions.List(0)
	t.Logf("Sessions after restart: %s", time.Since(start))
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("heap after Sessions: %.2f MiB", float64(mem.HeapAlloc)/(1<<20))
	for _, p := range []usage.Period{usage.Month, usage.All} {
		for _, phase := range []string{"first", "repeat"} {
			start = time.Now()
			page := ledgerPage(p, usage.Filter{}, 0, 100)
			t.Logf("Requests %s %s: %s (%d rows)", p, phase, time.Since(start), page.Total)
			if p == usage.All && page.Total != 350_000 {
				t.Fatalf("wrong total: %d", page.Total)
			}
		}
	}
}
