package sessions

// Request metadata is kept in independent shards. Growing one session rewrites
// only that shard; listing sessions never loads it. Shards are disposable and
// are rebuilt from the agent's files after corruption or a format change.

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const callCacheVersion = "calls-v1"
const maxKeptCalls = 4096
const maxKeptFiles = 32

var callsMu sync.Mutex
var callCache = map[string]*callFile{}
var callCounts = map[string]int{} // small allocation hints, never call records
var callOrder []string
var callRoot string

func callCacheDir() string             { return filepath.Join(filepath.Dir(CachePath()), callCacheVersion) }
func callCachePath(path string) string { return filepath.Join(callCacheDir(), hashHead(path)+".gob") }

func resetCalls() {
	callsMu.Lock()
	defer callsMu.Unlock()
	callCache, callOrder = map[string]*callFile{}, nil
	callCounts = map[string]int{}
	callRoot = ""
}

func pruneCalls(files []file) {
	root := callCacheDir()
	if callRoot != root {
		callCache, callOrder, callRoot = map[string]*callFile{}, nil, root
		callCounts = map[string]int{}
	}
	on := make(map[string]bool, len(files))
	shards := make(map[string]bool, len(files))
	for _, f := range files {
		on[f.path] = true
		shards[hashHead(f.path)+".gob"] = true
	}
	// Deleted or moved source files must not leave their old metadata behind.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".gob") && !shards[e.Name()] {
			_ = os.Remove(filepath.Join(root, e.Name()))
		}
	}
	order := callOrder[:0]
	for _, path := range callOrder {
		if on[path] {
			order = append(order, path)
		} else {
			delete(callCache, path)
		}
	}
	callOrder = order
	for path := range callCounts {
		if !on[path] {
			delete(callCounts, path)
		}
	}
}

func keepCalls(path string, st *callFile) {
	callCounts[path] = len(st.Calls)
	delete(callCache, path)
	order := callOrder[:0]
	for _, p := range callOrder {
		if p != path {
			order = append(order, p)
		}
	}
	callOrder = order
	if len(st.Calls) > maxKeptCalls {
		return
	}
	n := len(st.Calls)
	for _, s := range callCache {
		n += len(s.Calls)
	}
	for len(callOrder) > 0 && (n > maxKeptCalls || len(callOrder) >= maxKeptFiles) {
		p := callOrder[0]
		n -= len(callCache[p].Calls)
		delete(callCache, p)
		callOrder = callOrder[1:]
	}
	callCache[path] = st
	callOrder = append(callOrder, path)
}

func readCalls(f file) *callFile {
	old := callCache[f.path]
	if old == nil {
		if in, err := os.Open(callCachePath(f.path)); err == nil {
			var st callFile
			if gob.NewDecoder(in).Decode(&st) == nil && st.Path == f.path && st.Agent == f.agent {
				old = &st
			}
			in.Close()
		}
	}
	if old != nil && old.Size == f.size && old.Mod == f.mod.UnixNano() {
		keepCalls(f.path, old)
		return old
	}
	st := prepareCalls(f, old)
	off, err := scanAt(f.path, st.Off, func(b []byte) bool { return callHead(st, b) }, func(b []byte, start, end int64) bool {
		st.At, st.End = start, end
		if f.agent == "codex" {
			codexCallLine(st, b)
		} else {
			claudeCallLine(st, b)
		}
		return true
	})
	if err == nil {
		st.Off, st.Size, st.Mod = off, f.size, f.mod.UnixNano()
		writeCalls(st)
	}
	keepCalls(f.path, st)
	return st
}

func writeCalls(st *callFile) {
	dir := callCacheDir()
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	out, err := os.CreateTemp(dir, "calls-*.tmp")
	if err != nil {
		return
	}
	defer os.Remove(out.Name())
	// The string intern table can be reconstructed as more lines arrive.
	c := *st
	c.Strs = nil
	err = gob.NewEncoder(out).Encode(&c)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		_ = os.Rename(out.Name(), callCachePath(st.Path))
	}
}
