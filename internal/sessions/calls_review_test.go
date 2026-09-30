package sessions

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCallsReuseSavedIndexAndContinue(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "-p", "sess1.jsonl")
	writeLines(t, path, claudeMsg("m1", "claude-sonnet-5", 1, 2, 3, 4, 1))
	original := Calls(time.Time{})
	old := cache[path].Calls
	Saved()
	Reset()
	mu.Lock()
	loadCache()
	restored := cache[path].Calls
	mu.Unlock()
	if !reflect.DeepEqual(original, restored.Calls) || restored.Off != old.Off {
		t.Fatal("per-call metadata/offsets did not survive restart")
	}
	if got := Calls(time.Time{}); !reflect.DeepEqual(got, original) || cache[path].Calls != restored {
		t.Fatal("unchanged file was parsed again after restart")
	}
	appendText(t, path, claudeMsg("m2", "claude-sonnet-5", 5, 6, 7, 8, 2)+"\n")
	if got := Calls(time.Time{}); len(got) != 2 || got[0].Input != 5 {
		t.Fatalf("continued parse %+v", got)
	}
	if len(restored.Calls) != 1 {
		t.Fatal("published snapshot mutated")
	}
	Saved()
	b, err := os.ReadFile(CachePath())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("no saved index")
	}
}

func TestCodexSessionIdentitySurvivesIndexRestart(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
	writeLines(t, path,
		`{"timestamp":"`+stamp(0)+`","type":"session_meta","payload":{"id":"thread","model_provider":"custom","creator_account_id":"old-account","creator_user_id":"old-user"}}`,
		tokenCountLine(2, cxUse(10, 0, 0, 5, 0), cxUse(10, 0, 0, 5, 0)))
	check := func() {
		t.Helper()
		cs := Calls(time.Time{})
		if len(cs) != 1 || cs[0].Upstream != "custom" || cs[0].AccountID != "old-account" || cs[0].UserID != "old-user" {
			t.Fatalf("session identity %+v", cs)
		}
	}
	check()
	Saved()
	Reset()
	check()
	appendText(t, path, tokenCountLine(3, cxUse(20, 0, 0, 10, 0), cxUse(10, 0, 0, 5, 0))+"\n")
	cs := Calls(time.Time{})
	if len(cs) != 2 || cs[0].AccountID != "old-account" || cs[0].Upstream != "custom" {
		t.Fatal("incremental call lost session identity")
	}
}

func TestCallsShareSessionScan(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "-p", "sess1.jsonl")
	writeLines(t, path, claudeMsg("m1", "claude-sonnet-5", 1, 2, 3, 4, 1))
	List(0)
	kept := cache[path]
	if kept.Calls == nil || len(kept.Calls.Calls) != 1 {
		t.Fatal("session scan did not index its calls")
	}
	Calls(time.Time{})
	if cache[path] != kept {
		t.Fatal("ledger repeated the session parse")
	}
}

func TestCallsConcurrentWithSessionReaders(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.claude, "projects", "-p", "sess1.jsonl")
	writeLines(t, path, claudeMsg("m1", "claude-sonnet-5", 1, 2, 3, 4, 1))
	// OpenCode's DB connection must survive a concurrent call-file refresh.
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db := ocMakeEmpty(t, dbPath, "")
	t.Setenv("OPENCODE_DB", dbPath)
	if _, err := db.Exec(`INSERT INTO session(id,project_id,slug,directory,title,version,time_created,time_updated) VALUES('s','p','s','/work','db session','1',1,1)`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 9; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 3 {
			case 0:
				if len(Calls(time.Time{})) != 1 {
					t.Error("concurrent Calls lost a call")
				}
			case 1:
				if len(List(0)) != 2 {
					t.Error("concurrent List lost a session")
				}
			case 2:
				StatsFor(30)
			}
		}(i)
	}
	wg.Wait()
}
