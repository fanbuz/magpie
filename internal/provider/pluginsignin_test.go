package provider

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's sign-in is followed like a built-in one: the page, then the
// code pasted back (a wrong one fails the sign-in, which starts again);
// done, its provider is there, and removing it signs out. A key signs in
// at once.
func TestPluginSignIn(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}

	in := map[string]string{"where": "work", "team": "blue"}
	wait := func(id string) SignInState {
		t.Helper()
		for range 100 {
			if st, _ := SignInStatus(id); st.State != "waiting" {
				return st
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the sign-in is still waiting")
		return SignInState{}
	}
	st, err := StartPluginSignIn("fakeco", 1, in)
	if err != nil || st.Agent != "fakeco" || st.State != "waiting" || !st.PasteCode || !strings.Contains(st.URL, "where=work") || st.Instructions != "Paste the code" {
		t.Fatalf("StartPluginSignIn = %+v, %v", st, err)
	}
	if err := SubmitSignInCallback(st.ID, " "); err == nil {
		t.Fatal("an empty code was taken")
	}
	if err := SubmitSignInCallback(st.ID, "bad"); err == nil {
		t.Fatal("a wrong code was taken")
	}
	if got := wait(st.ID); got.State != "failed" {
		t.Fatalf("after a wrong code: %+v", got)
	}
	if err := SubmitSignInCallback(st.ID, "good"); err == nil {
		t.Fatal("a failed sign-in took another code")
	}

	st, _ = StartPluginSignIn("fakeco", 1, in)
	if err := SubmitSignInCallback(st.ID, "good"); err != nil {
		t.Fatal(err)
	}
	if got := wait(st.ID); got.State != "done" || got.User != "me@fake" {
		t.Fatalf("after the code: %+v", got)
	}
	if p, err := Find("fakeco"); err != nil || !p.IsPlugin() {
		t.Fatalf("Find = %+v, %v", p, err)
	}
	if err := Delete("fakeco"); err != nil {
		t.Fatal(err)
	}
	if plugin.SignedIn("fakeco") {
		t.Fatal("removing the account didn't sign out")
	}
	if _, err := Find("fakeco"); err == nil {
		t.Fatal("fakeco is a provider after it was removed")
	}

	if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, "  "); err == nil {
		t.Fatal("an empty key was taken")
	}
	if id, err := PluginAPIKey(ctx, "fakeco", 0, nil, "k1"); err != nil || id != "fakeco" {
		t.Fatalf("PluginAPIKey = %q, %v", id, err)
	}
	if _, err := Find("fakeco"); err != nil {
		t.Fatal(err)
	}
}
