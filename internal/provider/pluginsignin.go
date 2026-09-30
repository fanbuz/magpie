package provider

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// StartPluginSignIn begins signing in to a plugin's provider (OpenCode's
// id) with its OAuth method, the method's questions answered: the page to
// open, then either the plugin notices on its own or the code the page
// shows is pasted back (SubmitSignInCallback). SignInStatus follows it,
// as it does a built-in sign-in; its Agent is the provider's magpie id.
// The account goes beside those signed in already, or replaces the one it
// is.
func StartPluginSignIn(id string, method int, inputs map[string]string) (SignInState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	a, err := plugin.Authorize(ctx, id, method, inputs, plugin.NewAccount)
	cancel()
	if err != nil {
		return SignInState{}, err
	}
	s := &signInFlow{done: make(chan struct{})}
	s.st = SignInState{ID: randomToken(9), Agent: PluginID(id), State: "waiting", URL: a.URL, Instructions: a.Instructions}
	wait, stop := context.WithTimeout(context.Background(), signInTimeout)
	s.stop = stop
	if a.Method == "code" {
		s.st.PasteCode, s.plugin = true, a.Session
		go func() {
			<-wait.Done()
			if errors.Is(wait.Err(), context.DeadlineExceeded) {
				s.finish(SignInState{State: "failed", Error: "the sign-in timed out; start it again"})
			}
		}()
	} else {
		go func() {
			saved, err := plugin.Finish(wait, a.Session, "")
			s.pluginDone(saved, err)
		}()
	}
	signIns.Lock()
	for sid, o := range signIns.m {
		if o.st.Agent == s.st.Agent {
			o.finish(SignInState{State: "canceled"})
			delete(signIns.m, sid)
		}
	}
	signIns.m[s.st.ID] = s
	signIns.Unlock()
	return s.status(), nil
}

// pluginCode finishes a plugin's sign-in with the code its page showed.
func (s *signInFlow) pluginCode(code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return errors.New("paste the code the sign-in page showed")
	}
	s.mu.Lock()
	session := s.plugin
	s.plugin = ""
	waiting := s.st.State == "waiting"
	s.mu.Unlock()
	if session == "" || !waiting {
		return errors.New("this sign-in is over; start it again")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	saved, err := plugin.Finish(ctx, session, code)
	s.pluginDone(saved, err)
	return err
}

func (s *signInFlow) pluginDone(saved plugin.Saved, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		s.finish(SignInState{State: "canceled"})
	case errors.Is(err, context.DeadlineExceeded):
		s.finish(SignInState{State: "failed", Error: "the sign-in timed out; start it again"})
	case err != nil:
		s.finish(SignInState{State: "failed", Error: err.Error()})
	default:
		s.finish(SignInState{State: "done", User: pluginUser(saved)})
	}
}

// PluginAPIKey signs in to a plugin's provider with a key, as its "api"
// method does, beside the accounts signed in already; it gives the
// provider's magpie id.
func PluginAPIKey(ctx context.Context, id string, method int, inputs map[string]string, key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("enter the key")
	}
	saved, err := plugin.APIKey(ctx, id, method, inputs, strings.TrimSpace(key), plugin.NewAccount)
	if err != nil {
		return "", err
	}
	_ = ShowAccount(PluginID(saved.Provider))
	return PluginID(saved.Provider), nil
}

// pluginUser is the account signed in to, as the accounts list names it.
func pluginUser(saved plugin.Saved) string {
	for _, pp := range plugin.Cached() {
		if pp.ID == saved.Provider {
			return pluginLabels(pp)[saved.Account]
		}
	}
	return ""
}
