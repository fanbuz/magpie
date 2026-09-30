package provider

// Claude subscription requests must match Claude Code's wire identity. Anthropic
// uses these fields both for model-version gates and to attribute usage to the
// Pro/Max subscription rather than the extra-usage API bucket. This mirrors the
// current implementation in Alma's claude-subscription-service.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

var (
	claudeIdentityMu sync.Mutex
	claudeVersionAt  time.Time
	claudeVersion    = claudeVersionFloor
	claudeSession    string
)

var claudeSemverRE = regexp.MustCompile(`\d+\.\d+\.\d+`)

func compareClaudeVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var av, bv int
		if i < len(pa) {
			av, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			bv, _ = strconv.Atoi(pb[i])
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

// claudeVersionWait is how long a caller waits for `claude --version` before
// the version it has stands in, and claudeVersionTimeout how long the CLI is
// given at all. On Omarchy `claude` is a mise wrapper that installs or
// updates Claude Code before it answers, which can take minutes or never
// end; waited for with no limit, and with the lock held, it kept the
// Providers and Gateway pages (whose first look fetches the Claude
// account's models) and every Claude request waiting with it.
var (
	claudeVersionWait    = 3 * time.Second
	claudeVersionTimeout = time.Minute
	claudeVersionAsking  chan struct{} // closed when the CLI has answered; nil when it isn't being asked
)

// claudeClaimedVersion follows the locally installed auto-updating CLI, with a
// verified floor for machines where it cannot be found.
func claudeClaimedVersion() string {
	claudeIdentityMu.Lock()
	done := claudeVersionAsking
	if done == nil && time.Since(claudeVersionAt) >= 10*time.Minute {
		claudeVersionAt = time.Now()
		if path := claudeExecutable(); path != "" {
			done = make(chan struct{})
			claudeVersionAsking = done
			go askClaudeVersion(path, done)
		}
	}
	// only the first few seconds of an ask are waited for: after that the
	// version there is serves until the CLI answers
	wait := time.Until(claudeVersionAt.Add(claudeVersionWait))
	claudeIdentityMu.Unlock()
	if done != nil && wait > 0 {
		select {
		case <-done:
		case <-time.After(wait):
		}
	}
	claudeIdentityMu.Lock()
	defer claudeIdentityMu.Unlock()
	return claudeVersion
}

// askClaudeVersion runs `claude --version` and takes its answer, closing
// done when it is in.
func askClaudeVersion(path string, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), claudeVersionTimeout)
	defer cancel()
	out, err := proc.CommandContext(ctx, path, "--version").Output()
	claudeIdentityMu.Lock()
	defer claudeIdentityMu.Unlock()
	if err == nil {
		if installed := claudeSemverRE.FindString(string(out)); installed != "" && compareClaudeVersion(installed, claudeVersionFloor) > 0 {
			claudeVersion = installed
		} else {
			claudeVersion = claudeVersionFloor
		}
	}
	claudeVersionAsking = nil
	close(done)
}

func claudeUserAgent() string {
	return "claude-cli/" + claudeClaimedVersion() + " (external, cli)"
}

func randomUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func claudeSessionID() string {
	claudeIdentityMu.Lock()
	defer claudeIdentityMu.Unlock()
	if claudeSession == "" {
		claudeSession = randomUUID()
	}
	return claudeSession
}

func claudeDeviceID() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	path := filepath.Join(dir, "magpie", "claude-device-id")
	if b, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(b))
		if len(s) == 64 {
			return s
		}
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strings.Repeat("0", 64)
	}
	id := hex.EncodeToString(raw[:])
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(id+"\n"), 0o600)
	return id
}

func claudeFingerprint(text, version string) string {
	var chars strings.Builder
	for _, i := range []int{4, 7, 20} {
		if i < len(text) {
			chars.WriteByte(text[i])
		} else {
			chars.WriteByte('0')
		}
	}
	h := sha256.Sum256([]byte(claudeFingerprintSalt + chars.String() + version))
	return hex.EncodeToString(h[:])[:3]
}

func firstClaudeUserText(messages any) string {
	for _, raw := range asSlice(messages) {
		m, _ := raw.(map[string]any)
		if m["role"] != "user" {
			continue
		}
		if s, ok := m["content"].(string); ok {
			return s
		}
		for _, rawBlock := range asSlice(m["content"]) {
			b, _ := rawBlock.(map[string]any)
			if b["type"] == "text" {
				s, _ := b["text"].(string)
				return s
			}
		}
	}
	return ""
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func isClaudeSystemBlock(v any, needle string) bool {
	m, _ := v.(map[string]any)
	s, _ := m["text"].(string)
	return strings.Contains(s, needle)
}

// claudeBody adds Claude Code's billing/prefix/metadata identity and signs the
// final serialized body with the CCH hash. Existing genuine Claude Code blocks
// are retained but normalized to the currently claimed version.
func claudeBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}

	version := claudeClaimedVersion()
	var system []any
	switch v := m["system"].(type) {
	case string:
		if v != "" {
			system = append(system, map[string]any{"type": "text", "text": v})
		}
	case []any:
		system = append(system, v...)
	}
	var rest []any
	for _, block := range system {
		if !isClaudeSystemBlock(block, "x-anthropic-billing-header") && !isClaudeSystemBlock(block, "You are Claude Code") {
			rest = append(rest, block)
		}
	}
	billing := fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli; cch=00000;",
		version, claudeFingerprint(firstClaudeUserText(m["messages"]), version))
	prefix := map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude.",
		"cache_control": map[string]any{"type": "ephemeral"}}
	m["system"] = append([]any{map[string]any{"type": "text", "text": billing}, prefix}, rest...)

	metadata, _ := m["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	userID, _ := json.Marshal(map[string]string{"device_id": claudeDeviceID(), "account_uuid": "", "session_id": claudeSessionID()})
	metadata["user_id"] = string(userID)
	m["metadata"] = metadata

	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	const marker = "cch=00000;"
	if !bytes.Contains(out, []byte(marker)) {
		return out
	}
	hash := xxHash64(out, claudeCCHSeed) & 0xfffff
	repl := fmt.Sprintf("cch=%05x;", hash)
	return bytes.Replace(out, []byte(marker), []byte(repl), 1)
}

func claudeBetaHeader(model string) string {
	if strings.Contains(strings.ToLower(model), "haiku") {
		return claudeHaikuBetas
	}
	return claudeDefaultBetas
}

func claudeModelOf(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model
}

func claudeStainlessHeaders() map[string]string {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if arch == "" {
		arch = "x86"
	}
	operatingSystem := map[string]string{"darwin": "MacOS", "windows": "Windows", "freebsd": "FreeBSD"}[runtime.GOOS]
	if operatingSystem == "" {
		operatingSystem = "Linux"
	}
	return map[string]string{
		"X-Stainless-Lang": "js", "X-Stainless-Package-Version": claudeSDKVersion,
		"X-Stainless-Runtime": "node", "X-Stainless-Runtime-Version": claudeRuntimeVersion,
		"X-Stainless-Arch": arch, "X-Stainless-Os": operatingSystem,
		"X-Stainless-Timeout": "600", "X-Stainless-Retry-Count": "0",
	}
}

// xxHash64 is the exact XXH64 variant Claude Code uses for CCH body signing.
func xxHash64(b []byte, seed uint64) uint64 {
	const p1 uint64 = 11400714785074694791
	const p2 uint64 = 14029467366897019727
	const p3 uint64 = 1609587929392839161
	const p4 uint64 = 9650029242287828579
	const p5 uint64 = 2870177450012600261
	rot := func(x uint64, r uint) uint64 { return x<<r | x>>(64-r) }
	round := func(acc, input uint64) uint64 { acc += input * p2; acc = rot(acc, 31); return acc * p1 }
	merge := func(acc, v uint64) uint64 { v = round(0, v); acc ^= v; return acc*p1 + p4 }
	i := 0
	var h uint64
	if len(b) >= 32 {
		v1, v2, v3, v4 := seed+p1+p2, seed+p2, seed, seed-p1
		for i <= len(b)-32 {
			v1 = round(v1, binary.LittleEndian.Uint64(b[i:]))
			i += 8
			v2 = round(v2, binary.LittleEndian.Uint64(b[i:]))
			i += 8
			v3 = round(v3, binary.LittleEndian.Uint64(b[i:]))
			i += 8
			v4 = round(v4, binary.LittleEndian.Uint64(b[i:]))
			i += 8
		}
		h = rot(v1, 1) + rot(v2, 7) + rot(v3, 12) + rot(v4, 18)
		h = merge(h, v1)
		h = merge(h, v2)
		h = merge(h, v3)
		h = merge(h, v4)
	} else {
		h = seed + p5
	}
	h += uint64(len(b))
	for i+8 <= len(b) {
		k := round(0, binary.LittleEndian.Uint64(b[i:]))
		h ^= k
		h = rot(h, 27)*p1 + p4
		i += 8
	}
	if i+4 <= len(b) {
		h ^= uint64(binary.LittleEndian.Uint32(b[i:])) * p1
		h = rot(h, 23)*p2 + p3
		i += 4
	}
	for i < len(b) {
		h ^= uint64(b[i]) * p5
		h = rot(h, 11) * p1
		i++
	}
	h ^= h >> 33
	h *= p2
	h ^= h >> 29
	h *= p3
	h ^= h >> 32
	return h
}
