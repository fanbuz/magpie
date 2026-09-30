package provider

// Spending a Claude usage-limit reset by itself, for the accounts the user
// said may, by the rules a Codex account's are (codex_autoreset.go): only
// the week's own window being used up counts — not the five hours, nor a
// model's week — one a week at most, kept in claude-autoreset.json.

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

var claudeAutoReset = autoResets{file: "claude-autoreset.json"}

// ClaudeAutoReset says whether the Claude account user spends a reset by
// itself once its week is used up.
func ClaudeAutoReset(user string) bool {
	return user != "" && slices.Contains(settings.Load().ClaudeAutoReset, strings.ToLower(user))
}

// ClaudeSignedIn is the Claude account Claude Code is signed in to now.
func ClaudeSignedIn() (string, bool) {
	l, ok := liveLogin("claude")
	return l.User, ok
}

// AutoResets says whether agent's (Codex's or Claude's) account user
// spends its resets by itself.
func AutoResets(agent, user string) bool {
	switch agent {
	case "codex":
		return CodexAutoReset(user)
	case "claude":
		return ClaudeAutoReset(user)
	}
	return false
}

// SetClaudeAutoReset turns that on or off for user.
func SetClaudeAutoReset(user string, on bool) error {
	user = strings.ToLower(strings.TrimSpace(user))
	s := settings.Load()
	s.ClaudeAutoReset = slices.DeleteFunc(s.ClaudeAutoReset, func(u string) bool { return u == user })
	if on && user != "" {
		s.ClaudeAutoReset = append(s.ClaudeAutoReset, user)
	}
	return settings.Save(s)
}

// AutoUseClaudeReset spends one of the Claude account user's resets (the
// one Claude Code is signed in to when "") if the user turned that on for
// it, its week is used up, the reset Anthropic names next refills the
// week, and none was spent by itself in this week yet. Code is "" when
// none was tried; the caller asks again only on "reset".
func AutoUseClaudeReset(ctx context.Context, user string) (ResetOutcome, error) {
	if user == "" {
		live, ok := liveLogin("claude")
		if !ok {
			return ResetOutcome{}, nil
		}
		user = live.User
	}
	if !ClaudeAutoReset(user) {
		return ResetOutcome{}, nil
	}
	var who string
	return claudeAutoReset.use(user, func(now time.Time) (*time.Time, bool, error) {
		var tok string
		var err error
		who, tok, err = claudeUserToken(ViaLogin(ctx, "claude", user), user)
		if err != nil {
			return nil, false, err
		}
		// read afresh: what was read a while ago may not be used up yet;
		// what was, when Anthropic turns the usage endpoint away
		ws, grants, err := readClaudeUsage(ViaLogin(ctx, "claude", who), tok)
		if err != nil {
			claudeUsage.Lock()
			e, ok := claudeUsage.m[strings.ToLower(who)]
			claudeUsage.Unlock()
			if !ok || e.ws == nil {
				return nil, false, err
			}
			ws, grants = elapsed(e.ws, now), e.grants
		}
		g, ok := grants.next(now)
		return claudeWeekUsedUp(ws, now), ok && slices.Contains(g.Clears, "seven_day"), nil
	}, func() (ResetOutcome, error) { return UseClaudeReset(ctx, who) })
}

// claudeWeekUsedUp is when the week's own window of ws ends if it is used
// up: a model's week (Opus, Fable) being used up leaves the others.
func claudeWeekUsedUp(ws []QuotaWindow, now time.Time) *time.Time {
	return weekUsedUp(slices.DeleteFunc(slices.Clone(ws), func(w QuotaWindow) bool { return w.Model != "" }), now)
}
