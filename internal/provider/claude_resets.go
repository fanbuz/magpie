package provider

// Claude usage-limit resets: grants a Claude subscription is given (for a
// model's launch, say) that each refill its usage windows at once. The
// usage reading tells of them when asked for them, as Claude Code asks
// (cedar_ember=1); Claude Code's /limit-reset spends one with
// POST /api/organizations/{org}/reset_rate_limits, the grant named, and
// so does magpie. Shown on the account's card like a Codex account's
// resets, and spent only when the user says so, or by itself once the
// week is used up if the user let it (claude_autoreset.go).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// claudeUsagePath is the usage endpoint as Claude Code asks it when it
// wants the resets too; skip_spend leaves out what was spent past the plan.
const claudeUsagePath = "/api/oauth/usage?cedar_ember=1&skip_spend=1"

// claudeResetProgram names the resets to Anthropic, as Claude Code does.
const claudeResetProgram = "cedar_ember"

// claudeGrantID is what Claude Code accepts as a grant's id before it
// sends it on.
var claudeGrantID = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

// claudeGrants is the usage reading's cedar_ember: whether the account
// may use resets at all, the grants it holds, and the one to use next.
type claudeGrants struct {
	Eligible bool          `json:"eligible"`
	Grants   []claudeGrant `json:"grants"`
	Next     *string       `json:"next_grant_id"`
}

type claudeGrant struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	ResetsLeft int      `json:"resets_left"`
	EndsAt     *string  `json:"ends_at"`
	Clears     []string `json:"clears"`
	Paused     bool     `json:"paused"`
	UsableNow  bool     `json:"usable_now"`
}

func claudeTime(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil
	}
	return &t
}

// held says whether g still has resets to use, now or before it ends.
func (g claudeGrant) held(now time.Time) bool {
	if g.ResetsLeft <= 0 || g.Paused || g.ID == "" {
		return false
	}
	if end := claudeTime(g.EndsAt); end != nil && !end.After(now) {
		return false
	}
	return true
}

// credits is the resets the account holds as a card shows them: their
// count, and when the one used next runs out. nil when it holds none, or
// may not use them.
func (c *claudeGrants) credits(now time.Time) *ResetCredits {
	if c == nil || !c.Eligible {
		return nil
	}
	out := &ResetCredits{}
	for _, g := range c.Grants {
		if !g.held(now) {
			continue
		}
		out.Count += g.ResetsLeft
		end := claudeTime(g.EndsAt)
		if end == nil {
			continue
		}
		if c.Next != nil && g.ID == *c.Next || out.Until == nil {
			out.Until = end
		}
	}
	if out.Count == 0 {
		return nil
	}
	return out
}

// next is the grant a reset is spent from, the one Anthropic names, as
// Claude Code does; false when there is none to use now.
func (c *claudeGrants) next(now time.Time) (claudeGrant, bool) {
	if c == nil || !c.Eligible || c.Next == nil {
		return claudeGrant{}, false
	}
	for _, g := range c.Grants {
		if g.ID == *c.Next && g.held(now) && g.UsableNow && claudeGrantID.MatchString(g.ID) {
			return g, true
		}
	}
	return claudeGrant{}, false
}

// claudeResetsOf is the resets the Claude account user holds, as its
// usage was last read.
func claudeResetsOf(user string) *ResetCredits {
	claudeUsage.Lock()
	e := claudeUsage.m[strings.ToLower(user)]
	claudeUsage.Unlock()
	return e.grants.credits(time.Now())
}

// UseClaudeReset spends one of the usage-limit resets of the Claude
// account user (the one Claude Code is signed in to when ""): the grant
// Anthropic names next, which refills the windows it clears at once. It
// can't be undone: callers ask first. The account's usage is read afresh
// after.
func UseClaudeReset(ctx context.Context, user string) (ResetOutcome, error) {
	who, tok, err := claudeUserToken(ViaLogin(ctx, "claude", user), user)
	if err != nil {
		return ResetOutcome{}, err
	}
	ctx = ViaLogin(ctx, "claude", who)
	key := strings.ToLower(who)
	// which grant is next, read afresh; what was read last when Anthropic
	// turns the usage endpoint away, as it often does
	_, grants, err := readClaudeUsage(ctx, tok)
	if err != nil {
		claudeUsage.Lock()
		grants = claudeUsage.m[key].grants
		claudeUsage.Unlock()
		if grants == nil {
			return ResetOutcome{}, fmt.Errorf("couldn't read which reset is next, so none was used: %w", err)
		}
	}
	g, ok := grants.next(time.Now())
	if !ok {
		return ResetOutcome{Code: "no_credit"}, nil
	}
	p, err := claudeProfile(ctx, tok)
	if err != nil || p.Organization.UUID == "" {
		return ResetOutcome{}, fmt.Errorf("couldn't read the account's organization, so no reset was used: %v", err)
	}
	out, err := claimClaudeReset(ctx, tok, p.Organization.UUID, g.ID, newRedeemID())
	if err != nil {
		return out, err
	}
	if out.Code == "reset" && out.Windows == 0 {
		out.Windows = len(g.Clears)
	}
	claudeUsage.Lock()
	if e, ok := claudeUsage.m[key]; ok {
		// read again next time; what Claude Code said as it answered is
		// of before the reset
		e.at, e.heard, e.retry = time.Time{}, time.Time{}, time.Time{}
		if out.Code == "reset" {
			ws := slices.Clone(e.ws)
			for i, w := range ws {
				for _, k := range claudeKinds {
					if k.name == w.Name && slices.Contains(g.Clears, k.kind) {
						ws[i].Used, ws[i].ResetsAt = 0, nil
					}
				}
			}
			e.ws = ws
		}
		claudeUsage.m[key] = e
	}
	claudeUsage.Unlock()
	StaleAllowance("claude", who)
	if out.Code == "reset" {
		renewedNow("claude", who)
	}
	subscriptionUsageCache.Lock()
	subscriptionUsageCache.at = time.Time{}
	subscriptionUsageCache.data = nil
	subscriptionUsageCache.Unlock()
	return out, nil
}

// claudeUserToken signs in as the Claude account user: the one Claude
// Code itself is signed in to, or one magpie keeps — and says which.
func claudeUserToken(ctx context.Context, user string) (who, tok string, err error) {
	if live, ok := liveLogin("claude"); user == "" || ok && strings.EqualFold(live.User, user) {
		tok, err = claudeToken(ctx)
		return live.User, tok, err
	}
	for _, l := range Logins("claude") {
		if strings.EqualFold(l.User, user) {
			tok, _, err = savedLoginToken(ctx, "claude", l.User)
			return l.User, tok, err
		}
	}
	return "", "", fmt.Errorf("no Claude account %q", user)
}

// PointClaudeAt sends what magpie asks Anthropic's API itself (usage,
// resets, the profile) to base instead, the usage read so far forgotten,
// and says how to put it back: for tests outside this package.
func PointClaudeAt(base string) (restore func()) {
	was := claudeBase
	claudeBase = base
	forget := func() {
		claudeUsage.Lock()
		claudeUsage.m = nil
		claudeUsage.Unlock()
	}
	forget()
	return func() { claudeBase = was; forget() }
}

// claimClaudeReset spends a reset from grant; id makes a retry of the same
// request spend it once. The request is Claude Code's own.
func claimClaudeReset(ctx context.Context, token, org, grant, id string) (ResetOutcome, error) {
	var out ResetOutcome
	body, _ := json.Marshal(map[string]string{"program": claudeResetProgram, "grant_id": grant, "request_id": id})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeBase+"/api/organizations/"+url.PathEscape(org)+"/reset_rate_limits", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusTooManyRequests {
		return out, errors.New("Claude reset: Anthropic is rate limiting it; try again in a while")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return out, fmt.Errorf("Claude reset: %s", http.StatusText(res.StatusCode))
	}
	var wire struct {
		Result  string   `json:"result"`
		Cleared []string `json:"cleared"`
	}
	if err := json.Unmarshal(b, &wire); err != nil || wire.Result == "" {
		return out, errors.New("Claude reset: an answer that says nothing")
	}
	return ResetOutcome{Code: wire.Result, Windows: len(wire.Cleared)}, nil
}
