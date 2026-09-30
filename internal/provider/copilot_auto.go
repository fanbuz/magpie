package provider

// Copilot's Auto: the model Copilot picks for the account, the only one a
// Student plan (Copilot Student / edu) may choose. Copilot's /models doesn't
// list it; its clients add it to their pickers themselves (Copilot CLI
// 1.0.79: {id:"auto", name:"Auto"} first, its AUTO_MODEL_ID) and, when it is
// picked, ask POST {api}/models/session with
// {"auto_mode":{"model_hints":["auto"]}} for a session: a session_token, the
// selected_model the requests then name, the available_models and when it
// expires_at (unix seconds). Every request of the session carries the token
// in Copilot-Session-Token, with the selected model in its body.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// CopilotAuto is the model id that has Copilot pick the model.
const CopilotAuto = "auto"

// copilotAutoModel is Auto as magpie lists it.
var copilotAutoModel = catalog.Model{ID: CopilotAuto, Name: "Auto"}

type copilotAutoSession struct {
	Token     string
	Model     string // the model Copilot picked
	ExpiresAt int64
}

var (
	copilotAutoMu       sync.Mutex
	copilotAutoSessions = map[string]copilotAutoSession{} // by GitHub token
)

type copilotAutoKey struct{}

// copilotAutoResolve is the account's Auto session, asked again a couple of
// minutes before it expires or when fresh is set.
func copilotAutoResolve(ctx context.Context, app copilotApp, fresh bool) (copilotAutoSession, error) {
	copilotAutoMu.Lock()
	defer copilotAutoMu.Unlock()
	if a, ok := copilotAutoSessions[app.Token]; ok && !fresh && (a.ExpiresAt == 0 || time.Until(time.Unix(a.ExpiresAt, 0)) > 2*time.Minute) {
		return a, nil
	}
	s, err := app.session(ctx)
	if err != nil {
		return copilotAutoSession{}, err
	}
	base := s.Endpoints.API
	if base == "" {
		base = copilotBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/models/session", strings.NewReader(`{"auto_mode":{"model_hints":["auto"]}}`))
	if err != nil {
		return copilotAutoSession{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers() {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotAutoSession{}, errors.New("Copilot Auto: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var v struct {
		Token     string          `json:"session_token"`
		Selected  json.RawMessage `json:"selected_model"`
		ExpiresAt int64           `json:"expires_at"`
	}
	if res.StatusCode/100 != 2 || json.Unmarshal(b, &v) != nil || v.Token == "" {
		return copilotAutoSession{}, errors.New("Copilot Auto: " + APIError(b, res.Status))
	}
	// a model's id, or (Auto v2) the model itself
	var model string
	if json.Unmarshal(v.Selected, &model) != nil {
		var m struct {
			ID string `json:"id"`
		}
		json.Unmarshal(v.Selected, &m)
		model = m.ID
	}
	if model == "" {
		return copilotAutoSession{}, errors.New("Copilot Auto picked no model")
	}
	a := copilotAutoSession{Token: v.Token, Model: model, ExpiresAt: v.ExpiresAt}
	copilotAutoSessions[app.Token] = a
	return a, nil
}

// ResolveAuto turns a request for a model the account picks itself
// (Copilot's Auto) into one for the model it picked: ctx then carries what
// the request is signed with. Any other model comes back as it was.
func (p Provider) ResolveAuto(ctx context.Context, model string) (context.Context, string, error) {
	if p.Account == nil || p.Account.auto == nil || model != CopilotAuto {
		return ctx, model, nil
	}
	a, err := p.Account.auto(p.Via(ctx))
	if err != nil {
		return ctx, model, err
	}
	return context.WithValue(ctx, copilotAutoKey{}, a), a.Model, nil
}

// copilotAutoSign puts the Auto session on a request: the one ctx carries
// when the body names its model, or, for a body asking for "auto" itself
// (a test of the model), a session asked for here with the body's model
// swapped for the one picked.
func copilotAutoSign(ctx context.Context, app copilotApp, req *http.Request, body []byte) (model string, err error) {
	model = bodyModel(body)
	if a, ok := ctx.Value(copilotAutoKey{}).(copilotAutoSession); ok && a.Model == model {
		req.Header.Set("Copilot-Session-Token", a.Token)
		return model, nil
	}
	if model != CopilotAuto {
		return model, nil
	}
	a, err := copilotAutoResolve(ctx, app, false)
	if err != nil {
		return model, err
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return model, errors.New("Copilot Auto: the request isn't JSON")
	}
	m["model"], _ = json.Marshal(a.Model)
	nb, _ := json.Marshal(m)
	req.Body = io.NopCloser(bytes.NewReader(nb))
	req.ContentLength = int64(len(nb))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(nb)), nil }
	req.Header.Set("Copilot-Session-Token", a.Token)
	return a.Model, nil
}

// copilotSeen keeps the endpoints of every chat model Copilot's list
// named, listed by magpie or not: the model Auto picks may be one the
// account can't pick by hand, and is served on its own APIs all the same.
var (
	copilotSeenMu sync.Mutex
	copilotSeen   = map[string][]string{}
)

func copilotSeenAPIs(model string) []Protocol {
	copilotSeenMu.Lock()
	defer copilotSeenMu.Unlock()
	var out []Protocol
	for _, a := range copilotSeen[model] {
		out = append(out, Protocol(a))
	}
	return out
}
