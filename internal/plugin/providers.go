package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/settings"
)

// Method is a way a plugin signs in: "oauth" (a browser, then a code
// pasted back or not) or "api" (a key).
type Method struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}

// Model is a model a plugin's provider serves, as OpenCode lists it.
type Model struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	NPM       string   `json:"npm"` // the AI SDK package it is spoken to with
	URL       string   `json:"url"`
	APIID     string   `json:"apiId"`
	Context   int      `json:"context"`
	Input     int      `json:"input"`
	Output    int      `json:"output"`
	Reasoning bool     `json:"reasoning"`
	Image     bool     `json:"image"`
	Released  string   `json:"released"`
	Variants  []string `json:"variants"`
	Cost      *struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
}

// Provider is a provider a plugin signs in to.
type Provider struct {
	ID        string   `json:"id"`   // OpenCode's: google, github-copilot
	Spec      string   `json:"spec"` // the plugin
	Name      string   `json:"name"`
	NPM       string   `json:"npm"`
	API       string   `json:"api"`
	Methods   []Method `json:"methods"`
	SignedIn  bool     `json:"signedIn"`
	AuthType  string   `json:"authType"`
	AccountID string   `json:"accountId"`
	Models    []Model  `json:"models"`
	// Accounts are the accounts signed in to it, the one kept under its
	// own id first; SignedIn, AuthType and AccountID are that one's.
	Accounts []Account `json:"accounts"`
}

// Account is one account a provider is signed in to: Key is where
// plugin-auth.json keeps it (the provider's id, else id#slot).
type Account struct {
	Key       string `json:"key"`
	Type      string `json:"type"`
	AccountID string `json:"accountId"`
	// Hint tells an account with no id from another: the end of its key.
	Hint string `json:"hint,omitempty"`
}

var (
	provMu    sync.Mutex
	provCache []Provider
	provGood  bool
)

func providersPath() string { return filepath.Join(settings.Dir(), "plugin-providers.json") }

func forgetProviders() {
	provMu.Lock()
	provGood = false
	provMu.Unlock()
}

// Providers asks the plugins for their providers, starting the host if
// need be, and keeps the answer for Cached.
func Providers(ctx context.Context) ([]Provider, error) {
	var ps []Provider
	if err := Call(ctx, "providers", nil, &ps); err != nil {
		return nil, err
	}
	provMu.Lock()
	provCache, provGood = ps, true
	provMu.Unlock()
	if b, err := json.Marshal(ps); err == nil {
		_ = os.WriteFile(providersPath(), b, 0o600)
	}
	return ps, nil
}

// refreshing is Cached's refreshes in the background.
var refreshing sync.WaitGroup

// Settle waits for Cached's refreshes to end, then stops the host: for
// tests, whose folders the host runs in go when they end.
func Settle() {
	refreshing.Wait()
	Restart()
}

// Cached is the plugins' providers as last asked, without starting the
// host: what is known of them when magpie has only just started. A
// provider's sign-in is read afresh from plugin-auth.json.
func Cached() []Provider {
	provMu.Lock()
	ps := provCache
	good := provGood
	provMu.Unlock()
	if ps == nil {
		if b, err := os.ReadFile(providersPath()); err == nil {
			_ = json.Unmarshal(b, &ps)
		}
	}
	if len(Load().Plugins) == 0 {
		return nil
	}
	auth := readAuth()
	out := make([]Provider, 0, len(ps))
	on := map[string]bool{}
	for _, e := range Load().Plugins {
		if !e.Off {
			on[e.Spec] = true
		}
	}
	for _, p := range ps {
		if !on[p.Spec] {
			continue
		}
		p.Accounts = accountsOf(auth, p.ID)
		p.SignedIn = len(p.Accounts) > 0
		p.AuthType = ""
		if p.SignedIn {
			p.AuthType = p.Accounts[0].Type
			p.AccountID = p.Accounts[0].AccountID
		}
		out = append(out, p)
	}
	if !good && len(Load().Plugins) > 0 {
		// refreshed in the background: a sign-in or the plugins changed
		refreshing.Add(1)
		go func() {
			defer refreshing.Done()
			if Running() || HasBun() {
				_, _ = Providers(context.Background())
			}
		}()
		provMu.Lock()
		provGood = true // one refresh at a time; a failure is retried on the next change
		provMu.Unlock()
	}
	return out
}

type storedAuth struct {
	Type      string `json:"type"`
	AccountID string `json:"accountId"`
	Email     string `json:"email"`
	Key       string `json:"key"`
	Metadata  struct {
		Email string `json:"email"`
	} `json:"metadata"`
}

// ProviderOf is the provider an account key is one of.
func ProviderOf(key string) string {
	id, _, _ := strings.Cut(key, "#")
	return id
}

// accountsOf are provider's accounts in auth, as the host lists them.
func accountsOf(auth map[string]storedAuth, provider string) []Account {
	var out []Account
	for k, a := range auth {
		if ProviderOf(k) != provider {
			continue
		}
		who := firstNonEmpty(a.AccountID, a.Metadata.Email, a.Email)
		acct := Account{Key: k, Type: a.Type, AccountID: who}
		if a.Type == "api" && len(a.Key) >= 12 {
			acct.Hint = a.Key[len(a.Key)-4:]
		}
		out = append(out, acct)
	}
	slices.SortFunc(out, func(a, b Account) int {
		switch {
		case a.Key == provider:
			return -1
		case b.Key == provider:
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func readAuth() map[string]storedAuth {
	var m map[string]storedAuth
	if b, err := os.ReadFile(AuthPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// SignedIn is whether a plugin sign-in is kept for provider.
func SignedIn(provider string) bool {
	return len(accountsOf(readAuth(), provider)) > 0
}

// Prompt is a question a sign-in method asks before it starts.
type Prompt struct {
	Type        string `json:"type"` // text, select
	Key         string `json:"key"`
	Message     string `json:"message"`
	Placeholder string `json:"placeholder,omitempty"`
	Options     []struct {
		Label string `json:"label"`
		Value string `json:"value"`
		Hint  string `json:"hint,omitempty"`
	} `json:"options,omitempty"`
}

// NextPrompt is the method's next question given the answers so far; nil
// when it has asked them all.
func NextPrompt(ctx context.Context, provider string, method int, inputs map[string]string) (*Prompt, error) {
	var r struct {
		Prompt *Prompt `json:"prompt"`
	}
	err := Call(ctx, "prompt", map[string]any{"provider": provider, "method": method, "inputs": inputs}, &r)
	return r.Prompt, err
}

// Validate is what the method says is wrong with value as the answer to
// key, "" when nothing is.
func Validate(ctx context.Context, provider string, method int, key, value string) (string, error) {
	var r struct {
		Error *string `json:"error"`
	}
	if err := Call(ctx, "validate", map[string]any{"provider": provider, "method": method, "key": key, "value": value}, &r); err != nil {
		return "", err
	}
	if r.Error == nil {
		return "", nil
	}
	return *r.Error, nil
}

// Authorization is an OAuth sign-in begun: the page to open, and whether
// the plugin waits for it itself ("auto") or needs the code the page
// shows pasted back ("code").
type Authorization struct {
	Session      string `json:"session"`
	URL          string `json:"url"`
	Instructions string `json:"instructions"`
	Method       string `json:"method"`
}

// NewAccount is the account a sign-in goes to when it is a new one: the
// provider's own id while nothing is kept there. A sign-in to an account
// already signed in replaces that one's instead.
const NewAccount = "new"

// Authorize begins an OAuth sign-in to account (a key, or NewAccount).
func Authorize(ctx context.Context, provider string, method int, inputs map[string]string, account string) (Authorization, error) {
	var a Authorization
	err := Call(ctx, "authorize", map[string]any{"provider": provider, "method": method, "inputs": inputs, "account": account}, &a)
	return a, err
}

// Saved is where a sign-in was kept: the provider (a plugin may sign in
// to another than asked) and the account's key.
type Saved struct {
	Provider string `json:"provider"`
	Account  string `json:"account"`
}

// ErrFailed is a sign-in the plugin says failed.
var ErrFailed = errors.New("the sign-in failed")

// Finish waits for an OAuth sign-in to finish: an "auto" one on its own,
// a "code" one with the code pasted back. It gives where the sign-in was
// saved.
func Finish(ctx context.Context, session, code string) (Saved, error) {
	var r struct {
		OK bool `json:"ok"`
		Saved
	}
	if err := Call(ctx, "callback", map[string]any{"session": session, "code": code}, &r); err != nil {
		return Saved{}, err
	}
	if !r.OK {
		return Saved{}, ErrFailed
	}
	return r.Saved, nil
}

// APIKey signs in to account (a key, or NewAccount) with a key, as an
// "api" method does.
func APIKey(ctx context.Context, provider string, method int, inputs map[string]string, key, account string) (Saved, error) {
	var r struct {
		OK bool `json:"ok"`
		Saved
	}
	if err := Call(ctx, "apiKey", map[string]any{"provider": provider, "method": method, "inputs": inputs, "key": key, "account": account}, &r); err != nil {
		return Saved{}, err
	}
	if !r.OK {
		return Saved{}, ErrFailed
	}
	return r.Saved, nil
}

// SignOut forgets one account of the provider's, every one when account
// is "".
func SignOut(ctx context.Context, provider, account string) error {
	if Running() {
		return Call(ctx, "signOut", map[string]any{"provider": provider, "account": account}, nil)
	}
	var m map[string]json.RawMessage
	b, err := os.ReadFile(AuthPath())
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k := range m {
		if k == account || account == "" && ProviderOf(k) == provider {
			delete(m, k)
		}
	}
	b, _ = json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(AuthPath(), append(b, '\n'), 0o600); err != nil {
		return err
	}
	changed()
	return nil
}

// Options is what the provider's auth loader gave: where its requests
// go, the key the AI SDK would send, and whether it carries them itself.
type Options struct {
	BaseURL string            `json:"baseURL"`
	APIKey  string            `json:"apiKey"`
	Headers map[string]string `json:"headers"`
	Fetch   bool              `json:"fetch"`
}

var (
	optMu    sync.Mutex
	optCache = map[string]Options{}
	optGen   int64
)

// LoaderOptions runs the provider's auth loader for account (once per
// sign-in and host) and gives what it returned; account "" is the
// provider's first.
func LoaderOptions(ctx context.Context, provider, account string) (Options, error) {
	ck := provider + "\x00" + account
	optMu.Lock()
	if optGen != generation.Load() {
		optCache, optGen = map[string]Options{}, generation.Load()
	}
	o, ok := optCache[ck]
	optMu.Unlock()
	if ok {
		return o, nil
	}
	if err := Call(ctx, "load", map[string]any{"provider": provider, "account": account}, &o); err != nil {
		return Options{}, err
	}
	optMu.Lock()
	if optGen == generation.Load() {
		optCache[ck] = o
	}
	optMu.Unlock()
	return o, nil
}

func init() {
	OnChange(func() {
		optMu.Lock()
		optCache = map[string]Options{}
		optMu.Unlock()
	})
}
