package provider

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's accounts show their allowance as the built-ins' do: beside
// each account, on the usage page, and to the gateway, a window counting
// only some models holding back those alone.
func TestPluginUsage(t *testing.T) {
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
	for _, team := range []string{"a", "full"} {
		st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": team})
		if err != nil {
			t.Fatal(err)
		}
		if err := SubmitSignInCallback(st.ID, "good"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PluginAPIKey(ctx, "fakeco", 0, nil, "k-123456"); err != nil {
		t.Fatal(err)
	}

	for _, agent := range []string{"fakeco", "plugin:fakeco"} {
		u := LoginUsage(ctx, agent)
		if len(u) != 3 {
			t.Fatalf("%s: %d accounts' usage, want 3: %+v", agent, len(u), u)
		}
		q := u["a@fake"]
		if q.Error != "" || q.Plan != "Fake Pro" || q.Provider != "fakeco" || q.Name != "FakeCo" || len(q.Windows) != 3 {
			t.Fatalf("%s: a@fake = %+v", agent, q)
		}
		if q.Until == nil || q.Until.Year() != 2030 || q.Renew != "auto" {
			t.Fatalf("%s: plan period %v %q", agent, q.Until, q.Renew)
		}
		w, week, extra := q.Windows[0], q.Windows[1], q.Windows[2]
		if w.Name != "5 hours" || w.Used != 25 || w.Span != 5*time.Hour || w.ResetsAt == nil || time.Until(*w.ResetsAt) < 50*time.Minute {
			t.Fatalf("%s: five hours = %+v", agent, w)
		}
		// a reset in seconds reads as one
		if week.ResetsAt == nil || time.Until(*week.ResetsAt) < 23*time.Hour || time.Until(*week.ResetsAt) > 25*time.Hour {
			t.Fatalf("%s: week = %+v", agent, week)
		}
		if extra.Used != 250 || extra.Display != "$2.50" || !extra.Aside {
			t.Fatalf("%s: extra = %+v", agent, extra)
		}
		if e := u["API key"].Error; e != "an API key has no plan" {
			t.Fatalf("%s: the key's usage error = %q (%v)", agent, e, u)
		}
	}

	cards := 0
	for _, q := range fetchSubscriptionUsage() {
		if q.Provider == "fakeco" {
			cards++
			if q.Name != "FakeCo" || q.User == "" {
				t.Fatalf("usage card %+v", q)
			}
		}
	}
	if cards != 3 {
		t.Fatalf("%d usage cards for the plugin's accounts, want 3", cards)
	}

	// the gateway asks the allowance by the account's UsageAgent
	var agent string
	for _, p := range All() {
		if p.IsPlugin() && p.ID == "fakeco" {
			agent = p.Account.UsageAgent()
		}
	}
	if agent != "plugin:fakeco" {
		t.Fatalf("UsageAgent = %q", agent)
	}
	// the plan the usage told stays with the account, shown beside it
	plan := false
	for _, p := range All() {
		if p.IsPlugin() && p.ID == "fakeco" && p.Account.User == "a@fake" {
			plan = p.Account.Plan == "Fake Pro"
		}
	}
	if !plan {
		t.Fatal("a@fake's provider doesn't carry its plan")
	}
	if q := LoginUsage(ctx, agent)["full@fake"]; q.Resets == nil || !q.Resets.ByWindow || q.Resets.FiveHour != 2 || q.Resets.Weekly != 1 || q.Resets.Count != 3 || q.User != "Full@Fake.example" {
		t.Fatalf("full@fake = %+v, resets %+v", q, q.Resets)
	}
	full := allowanceOf(LoginUsage(ctx, agent)["full@fake"].Windows, time.Now())
	if used, _ := full.For("fake-claude", time.Now()); used != 100 {
		t.Fatalf("fake-claude on full@fake: used %v, want 100", used)
	}
	if used, _ := full.For("fake-1", time.Now()); used != 10 {
		t.Fatalf("fake-1 on full@fake: used %v, want 10 (the five hours don't count it)", used)
	}
	if full.Full("fake-claude", 100, time.Now()).IsZero() || !full.Full("fake-1", 100, time.Now()).IsZero() {
		t.Fatal("full@fake is used up for fake-claude alone")
	}
}
