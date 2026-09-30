package agent

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGeminiNotAntigravity: a ~/.gemini holding only what Antigravity keeps
// there is no Gemini CLI (#330); what Gemini CLI writes, or its binary, is.
func TestGeminiNotAntigravity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(home, ".gemini")
	for _, d := range []string{"antigravity", "config", "antigravity-cli"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	// Antigravity reads its global rules from ~/.gemini/GEMINI.md too
	os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte("rules"), 0o644)
	if gemini(home).Detected() {
		t.Fatal("Antigravity's folders taken for Gemini CLI")
	}
	if !agy(home).Detected() {
		t.Fatal("agy not detected from ~/.gemini/antigravity-cli")
	}

	// a sign-in Antigravity's OAuth client minted there isn't Gemini CLI's
	creds := filepath.Join(dir, "oauth_creds.json")
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"azp":"other.apps.googleusercontent.com","aud":"other.apps.googleusercontent.com"}`))
	os.WriteFile(creds, []byte(`{"refresh_token":"r","id_token":"e30.`+claims+`.sig"}`), 0o600)
	if gemini(home).Detected() {
		t.Fatal("another app's sign-in taken for Gemini CLI")
	}
	os.WriteFile(creds, []byte(`{"refresh_token":"r"}`), 0o600)
	if !gemini(home).Detected() {
		t.Fatal("Gemini CLI's sign-in not detected")
	}
	os.Remove(creds)

	for _, f := range []string{"settings.json", ".env", "google_accounts.json", "installation_id"} {
		p := filepath.Join(dir, f)
		os.WriteFile(p, []byte("{}"), 0o644)
		if !gemini(home).Detected() {
			t.Fatalf("%s not taken for Gemini CLI", f)
		}
		os.Remove(p)
	}

	if runtime.GOOS == "windows" {
		return
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "gemini"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin)
	if !gemini(home).Detected() {
		t.Fatal("gemini on PATH not detected")
	}
}
