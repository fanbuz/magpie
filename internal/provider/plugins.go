package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
)

// A plugin's provider (see internal/plugin) is a subscription like the
// built-in ones: signed in to through the plugin, its models the ones the
// plugin lists, its requests made by the plugin's fetch. It speaks what
// OpenCode would speak to each model, by the AI SDK package the model is
// spoken to with.

// pluginBase is where a plugin provider's requests seem to go, for the
// gateway: its transport takes them to where the plugin says instead.
const pluginBase = "plugin://"

// ConversationHeader carries the conversation a request belongs to, to a
// plugin's chat.headers hook as its session; it goes no further.
const ConversationHeader = "X-Magpie-Conversation"

// PluginID is the id magpie gives the provider OpenCode calls id: the same,
// unless a preset or a built-in subscription has it (google, openai,
// anthropic), when it is id-plugin.
func PluginID(id string) string {
	if slices.Contains(accountIDs, id) || Preset(id) != nil || id == "magpie" {
		return id + "-plugin"
	}
	return id
}

// IsPlugin is whether the provider is a plugin's.
func (p Provider) IsPlugin() bool { return p.Account != nil && p.Account.plugin != nil }

// PluginOf is the plugin provider magpie's provider id is, when it is one.
func PluginOf(id string) (plugin.Provider, bool) {
	for _, pp := range plugin.Cached() {
		if PluginID(pp.ID) == id {
			return pp, true
		}
	}
	return plugin.Provider{}, false
}

// pluginAccounts are the plugins' providers signed in to.
func pluginAccounts() []Provider {
	var out []Provider
	for _, pp := range plugin.Cached() {
		if pp.SignedIn {
			out = append(out, pluginProvider(pp))
		}
	}
	return out
}

// pluginProtocol is the API model is spoken to on, as OpenCode's AI SDK
// package for it speaks: Anthropic's, OpenAI's Responses (and Copilot's
// for GPT-5 and after), Gemini's (as Code Assist's inner request), else
// chat completions.
func pluginProtocol(provider string, m plugin.Model) Protocol {
	switch m.NPM {
	case "@ai-sdk/anthropic", "@ai-sdk/google-vertex/anthropic":
		return Anthropic
	case "@ai-sdk/openai", "@ai-sdk/azure":
		return Responses
	case "@ai-sdk/google":
		return CodeAssist
	case "@ai-sdk/github-copilot":
		return copilotAPI(modelAPIID(m))
	}
	if strings.HasPrefix(provider, "github-copilot") {
		return copilotAPI(modelAPIID(m))
	}
	return Chat
}

var gptN = regexp.MustCompile(`^gpt-(\d+)`)

// copilotAPI is OpenCode's shouldUseCopilotResponsesApi: GPT-5 and after,
// but for gpt-5-mini, on Responses.
func copilotAPI(model string) Protocol {
	if m := gptN.FindStringSubmatch(model); m != nil {
		if n, _ := strconv.Atoi(m[1]); n >= 5 && !strings.HasPrefix(model, "gpt-5-mini") {
			return Responses
		}
	}
	return Chat
}

// npmBase is where the AI SDK package sends a model by default.
var npmBase = map[string]string{
	"@ai-sdk/anthropic":      "https://api.anthropic.com/v1",
	"@ai-sdk/openai":         "https://api.openai.com/v1",
	"@ai-sdk/google":         "https://generativelanguage.googleapis.com/v1beta",
	"@ai-sdk/github-copilot": "https://api.githubcopilot.com",
	"@ai-sdk/xai":            "https://api.x.ai/v1",
	"@ai-sdk/mistral":        "https://api.mistral.ai/v1",
	"@ai-sdk/groq":           "https://api.groq.com/openai/v1",
	"@ai-sdk/deepseek":       "https://api.deepseek.com/v1",
}

func pluginModel(pp plugin.Provider, id string) (plugin.Model, bool) {
	for _, m := range pp.Models {
		if m.ID == id {
			return m, true
		}
	}
	return plugin.Model{}, false
}

func pluginCatalog(pp plugin.Provider) []catalog.Model {
	out := make([]catalog.Model, 0, len(pp.Models))
	for _, m := range pp.Models {
		c := catalog.Model{
			ID: m.ID, Name: m.Name, Provider: pp.ID, Released: m.Released,
			APIs: []string{string(pluginProtocol(pp.ID, m))}, Images: m.Image,
			Context: m.Input, Output: m.Output,
		}
		if c.Context == 0 {
			c.Context = m.Context
		}
		if c.Name == "" {
			c.Name = m.ID
		}
		if m.Reasoning {
			c.Efforts = m.Variants
		}
		if m.Cost != nil {
			c.Price = &catalog.Price{Input: m.Cost.Input, Output: m.Cost.Output}
		}
		out = append(out, c)
	}
	return out
}

func pluginProvider(pp plugin.Provider) Provider {
	id := PluginID(pp.ID)
	name := pp.Name
	if name == "" {
		name = pp.ID
	}
	user := pp.AccountID
	if user == "" {
		user = map[string]string{"api": "API key", "oauth": "Signed in"}[pp.AuthType]
	}
	a := &Account{Agent: "plugin", User: user, Stream: true, plugin: &pp}
	a.models = func() []catalog.Model {
		if cur, ok := PluginOf(id); ok {
			return pluginCatalog(cur)
		}
		return pluginCatalog(pp)
	}
	a.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ps, err := plugin.Providers(ctx)
		if err != nil {
			return nil, err
		}
		for _, cur := range ps {
			if cur.ID == pp.ID {
				return catalog.Chat(pluginCatalog(cur)), nil
			}
		}
		return nil, fmt.Errorf("%s's plugin no longer lists it", name)
	}
	a.sign = func(ctx context.Context, req *http.Request, body []byte) error { return nil }
	a.transport = func(req *http.Request) (*http.Response, error) { return pluginFetch(pp, req) }
	p := Provider{ID: id, Name: name, Account: a}
	for _, m := range pp.Models {
		switch pluginProtocol(pp.ID, m) {
		case Chat:
			p.Chat = pluginBase + pp.ID + "/v1"
		case Responses:
			p.Responses = pluginBase + pp.ID + "/v1"
		case Anthropic:
			p.Anthropic = pluginBase + pp.ID
		case CodeAssist:
			a.codeAssist = pluginBase + pp.ID
		}
	}
	return p
}

// pluginAPIs is the one API a plugin provider's model is spoken to on.
func (p Provider) pluginAPIs(model string) []Protocol {
	pp := p.Account.plugin
	if cur, ok := PluginOf(p.ID); ok {
		pp = &cur
	}
	if m, ok := pluginModel(*pp, model); ok {
		return []Protocol{pluginProtocol(pp.ID, m)}
	}
	return nil
}

// pluginFetch sends a request the gateway made for a plugin's provider
// through the plugin: to the base URL its loader gave (else the model's,
// the provider's, the AI SDK package's), with what the loader adds.
func pluginFetch(pp plugin.Provider, req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	rest := strings.TrimPrefix(req.URL.String(), pluginBase+pp.ID)
	model := bodyModel(body)
	codeAssist := strings.HasPrefix(rest, "/v1internal:")
	if codeAssist {
		// Code Assist's envelope off: Gemini's own request, the model in
		// the path, as @ai-sdk/google sends it
		var env struct {
			Model   string          `json:"model"`
			Request json.RawMessage `json:"request"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, err
		}
		model, body = env.Model, env.Request
	}
	m, ok := pluginModel(pp, model)
	if !ok {
		if cur, found := PluginOf(PluginID(pp.ID)); found {
			pp = cur
			m, ok = pluginModel(pp, model)
		}
	}
	if !ok {
		m = plugin.Model{ID: model, NPM: pp.NPM}
	}
	api := modelAPIID(m)
	if api != model && !codeAssist {
		body = withModel(body, api)
	}
	o, err := plugin.LoaderOptions(ctx, pp.ID)
	if err != nil {
		return nil, err
	}
	base := firstOf(o.BaseURL, m.URL, pp.API, npmBase[m.NPM])
	if base == "" {
		return nil, errors.New(pp.Name + "'s plugin says nowhere to send its requests")
	}
	base = strings.TrimRight(base, "/")
	var url string
	switch {
	case codeAssist:
		url = base + "/models/" + api + ":streamGenerateContent?alt=sse"
	case strings.HasPrefix(rest, "/v1/"):
		// chat, responses and Anthropic's messages: the AI SDK's base
		// ends where magpie's /v1 does
		url = base + strings.TrimPrefix(rest, "/v1")
	default:
		url = base + rest
	}
	h := map[string]string{}
	for k, vs := range req.Header {
		if len(vs) > 0 && !strings.EqualFold(k, ConversationHeader) {
			h[strings.ToLower(k)] = vs[0]
		}
	}
	return plugin.Fetch(ctx, plugin.FetchRequest{
		Provider: pp.ID, Model: api, NPM: m.NPM, URL: url, Method: req.Method,
		Headers: h, Body: body, Session: req.Header.Get(ConversationHeader),
	})
}

func modelAPIID(m plugin.Model) string {
	if m.APIID != "" {
		return m.APIID
	}
	return m.ID
}

func withModel(body []byte, model string) []byte {
	var v map[string]json.RawMessage
	if json.Unmarshal(body, &v) != nil {
		return body
	}
	v["model"], _ = json.Marshal(model)
	if b, err := json.Marshal(v); err == nil {
		return b
	}
	return body
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// Do sends req, through the account's own transport when it has one (a
// plugin's) and client otherwise.
func (p Provider) Do(client *http.Client, req *http.Request) (*http.Response, error) {
	if p.Account != nil && p.Account.transport != nil {
		return p.Account.transport(req)
	}
	req.Header.Del(ConversationHeader)
	return client.Do(req)
}
