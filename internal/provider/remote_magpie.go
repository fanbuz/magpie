package provider

import "strings"

// A remote magpie is another computer's magpie gateway, shared on its
// network (Settings → Share on local network), as a provider: the
// computers' agents stay wired by their own magpie, while the providers,
// routing groups and usage are the one magpie's they all share. Its
// address serves every API magpie does, so the provider speaks them all
// — Chat Completions and Responses at <address>/v1, Anthropic's Messages
// at the address — and a request goes on in the API the agent spoke. Its
// model list tells, of each model, the APIs its own provider serves it on
// (native_endpoints), and a request for one goes on one of those: turned
// into another API once, here, when it must be, never on both computers.

// RemoteMagpiePreset is the preset's id.
const RemoteMagpiePreset = "remote-magpie"

// IsRemoteMagpie reports whether the provider is another magpie.
func (p Provider) IsRemoteMagpie() bool { return p.Preset == RemoteMagpiePreset }

// remoteMagpieEndpoints puts every API on the address given, however it
// was typed: the bare host and port (plain http, which magpie serves),
// …/v1, or the Anthropic root.
func (p *Provider) remoteMagpieEndpoints() {
	if !p.IsRemoteMagpie() {
		return
	}
	root := ""
	for _, u := range []string{p.Chat, p.Responses, p.Anthropic} {
		if u = strings.TrimRight(strings.TrimSpace(u), "/"); u != "" {
			root = u
			break
		}
	}
	if root == "" {
		return
	}
	if !strings.Contains(root, "://") {
		root = "http://" + root
	}
	for _, suf := range []string{"/v1/messages", "/v1/chat/completions", "/v1/responses", "/v1"} {
		if b, ok := strings.CutSuffix(root, suf); ok {
			root = b
			break
		}
	}
	p.Chat, p.Responses, p.Anthropic = root+"/v1", root+"/v1", root
}
