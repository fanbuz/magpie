package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClaudeNativeReplayAcrossReadersAndRestart(t *testing.T) {
	data, err := os.ReadFile("testdata/claude-native-replay.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := Tokens{10338, 37363, 281066, 63398}
	for split := 0; split <= len(lines); split++ {
		t.Run(string(rune('0'+split)), func(t *testing.T) {
			d := setupCalls(t)
			path := filepath.Join(d.claude, "projects", "fixture", "session.jsonl")
			writeLines(t, path, lines[:split]...)
			Calls(time.Time{})
			List(0)
			Reset()
			if split < len(lines) {
				appendText(t, path, strings.Join(lines[split:], "\n")+"\n")
			}
			check := func(cs []Call) {
				t.Helper()
				var total Tokens
				for _, c := range cs {
					total.add(c.Tokens)
				}
				if len(cs) != 2 || total != want {
					t.Fatalf("calls=%d tokens=%+v, want 2 %+v", len(cs), total, want)
				}
			}
			cs := Calls(time.Time{})
			check(cs)
			for _, source := range CallSources() {
				if source.Path == path {
					check(ReadCallSource(source))
				}
			}
			if list := List(0); len(list) != 1 || list[0].Tokens != want {
				t.Fatalf("summary differs: %+v", list)
			}
			b, err := json.Marshal(cache[path])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "claude_usage") || strings.Contains(string(b), "blocks") || strings.Contains(string(b), "message_4") {
				t.Fatal("summary persisted message state")
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			shard := loadCalls(file{path: path, agent: "claude", size: info.Size(), mod: info.ModTime()})
			if shard == nil || shard.Claude != nil {
				t.Fatal("call shard persisted block/tool maps")
			}
			Reset()
			if again := Calls(time.Time{}); !reflect.DeepEqual(cs, again) {
				t.Fatal("restart changed native calls")
			}
		})
	}
}

func TestClaudeDesktopCopyOfMessageIsCountedOnce(t *testing.T) {
	d := setupCalls(t)
	line := claudeMsg("shared-message", "claude-opus-5-5", 10, 2, 30, 4, 1)
	writeLines(t, filepath.Join(d.claude, "projects", "fixture", "session.jsonl"), line)
	writeLines(t, filepath.Join(d.desktop, "local-agent-mode-sessions", "org", "workspace", "local_fixture", ".claude", "projects", "fixture", "session.jsonl"), line)
	if cs := Calls(time.Time{}); len(cs) != 1 {
		t.Fatalf("CLI/Desktop copies both counted: %+v", cs)
	}
}
