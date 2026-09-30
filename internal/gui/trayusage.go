package gui

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Settings → Usage in the menu bar: one subscription's or plan's windows
// beside the tray icon ("42% · 18%"), for keeping an eye on them without
// opening magpie. The Mac's menu bar shows the text; Windows' tray has no
// room for any, and gets it in the icon's tooltip only.

// trayUsageEvery is how often the text is brought up to date, as Settings
// says (every 3 minutes unless told otherwise). The vendors are asked no
// more than the Usage page asks them: their answers are cached for a
// minute, and this reads the cache.
func trayUsageEvery() time.Duration {
	return time.Duration(settings.Load().TrayUsageEvery) * time.Minute
}

// trayCardID names a card for settings.TrayUsage: its provider, and the
// account when there is one, as two of one vendor can be signed in.
func trayCardID(q provider.SubscriptionQuota) string {
	if q.User == "" {
		return q.Provider
	}
	return q.Provider + "|" + q.User
}

// trayWindows are the windows a card's text shows: those that stop the
// account (not on-demand spending, nor one that counts a single model's
// use), the shortest first where the vendor says how long they run.
func trayWindows(q provider.SubscriptionQuota) []provider.QuotaWindow {
	var ws []provider.QuotaWindow
	for _, w := range q.Windows {
		if !w.Aside && w.Model == "" {
			ws = append(ws, w)
		}
	}
	slices.SortStableFunc(ws, func(a, b provider.QuotaWindow) int {
		if a.Span == 0 || b.Span == 0 {
			return 0
		}
		return cmp.Compare(a.Span, b.Span)
	})
	return ws
}

// trayUsageText is the menu bar's text for a card and the tooltip that
// spells it out: each window's use, or what is left of it (left), and when
// it starts again.
func trayUsageText(q provider.SubscriptionQuota, now time.Time, left bool) (label, tip string) {
	if q.Error != "" {
		return "", q.Name + ": " + q.Error
	}
	ws := trayWindows(q)
	if len(ws) == 0 {
		if q.Balance == "" {
			return "", ""
		}
		return q.Balance, q.Name + " · " + q.Balance
	}
	var short, long []string
	for _, w := range ws {
		used := math.Max(0, math.Min(100, w.Used))
		n := int(math.Round(used))
		word := "used"
		if left {
			used = 100 - used
			n, word = 100-n, "left"
		}
		pct := fmt.Sprintf("%d%%", n)
		short = append(short, pct)
		tipPct := pct
		if used != math.Trunc(used) {
			tipPct = fmt.Sprintf("%.1f%%", used)
		}
		line := w.Name + " " + tipPct + " " + word
		if w.Display != "" {
			line = w.Name + " " + w.Display + " · " + tipPct + " " + word
		}
		if at := resetAt(w, now); !at.IsZero() && at.After(now) {
			line += " · resets in " + until(at.Sub(now))
		}
		long = append(long, line)
	}
	if len(short) > 2 {
		short = short[:2]
	}
	return strings.Join(short, " · "), q.Name + "\n" + strings.Join(long, "\n")
}

func resetAt(w provider.QuotaWindow, now time.Time) time.Time {
	if w.ResetsAt != nil {
		return *w.ResetsAt
	}
	if w.ResetSecs > 0 {
		return now.Add(time.Duration(w.ResetSecs) * time.Second)
	}
	return time.Time{}
}

// until says a wait in the largest two units: 3d 4h, 2h 10m, 7m.
func until(d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", max(mins, 1))
}

// trayUsageCard is the card settings.TrayUsage names, among the Usage
// page's (cached as there); false when it is off or gone.
func trayUsageCard(ctx context.Context) (provider.SubscriptionQuota, bool) {
	id := settings.Load().TrayUsage
	if id == "" {
		return provider.SubscriptionQuota{}, false
	}
	cards := provider.Quotas(ctx)
	i := slices.IndexFunc(cards, func(q provider.SubscriptionQuota) bool { return trayCardID(q) == id })
	if i < 0 {
		return provider.SubscriptionQuota{}, false
	}
	return cards[i], true
}

// onTrayUsage brings the tray's text up to date at once, when the Settings
// page changes which card it shows; set by the process that has the tray.
var onTrayUsage func()
