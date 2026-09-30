package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeClaudeVersion points claudeExecutable at a script, waited for as long
// as wait, and forgets the version asked last.
func fakeClaudeVersion(t *testing.T, script string, wait time.Duration) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for the CLI")
	}
	exe := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldExe, oldWait := claudeExecutable, claudeVersionWait
	claudeExecutable = func() string { return exe }
	claudeVersionWait = wait
	forget := func() {
		claudeIdentityMu.Lock()
		claudeVersionAt, claudeVersion, claudeVersionAsking = time.Time{}, claudeVersionFloor, nil
		claudeIdentityMu.Unlock()
	}
	forget()
	t.Cleanup(func() { claudeExecutable, claudeVersionWait = oldExe, oldWait; forget() })
}

// On Omarchy `claude` is a mise wrapper that installs Claude Code before it
// answers (Discord: on Omarchy the Providers and Gateway pages never loaded):
// a CLI that doesn't answer leaves the floor standing, after a moment, and
// nobody else waiting on it.
func TestClaudeVersionDoesNotWaitForAStalledCLI(t *testing.T) {
	fakeClaudeVersion(t, "exec sleep 30\n", 300*time.Millisecond)
	start := time.Now()
	if v := claudeClaimedVersion(); v != claudeVersionFloor {
		t.Fatalf("version %q, want the floor %q", v, claudeVersionFloor)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waited %v for a CLI that doesn't answer", d)
	}
	start = time.Now()
	if claudeSessionID() == "" || claudeClaimedVersion() != claudeVersionFloor {
		t.Fatal("no session id or version")
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("a second look waited %v", d)
	}
}

// A CLI that answers within the wait is still taken on the first look (a
// script's first run can take a moment on macOS).
func TestClaudeVersionFromTheCLI(t *testing.T) {
	fakeClaudeVersion(t, "echo '99.1.2 (Claude Code)'\n", 10*time.Second)
	if v := claudeClaimedVersion(); v != "99.1.2" {
		t.Fatalf("version %q, want the CLI's 99.1.2", v)
	}
}

// One that answers after the wait is taken once it has.
func TestClaudeVersionTakenWhenItComes(t *testing.T) {
	fakeClaudeVersion(t, "sleep 2\necho '99.1.3 (Claude Code)'\n", 300*time.Millisecond)
	if v := claudeClaimedVersion(); v != claudeVersionFloor {
		t.Fatalf("version %q before the CLI answered, want the floor", v)
	}
	deadline := time.Now().Add(10 * time.Second)
	for claudeClaimedVersion() != "99.1.3" {
		if time.Now().After(deadline) {
			t.Fatalf("version %q, want 99.1.3 once the CLI answered", claudeClaimedVersion())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
