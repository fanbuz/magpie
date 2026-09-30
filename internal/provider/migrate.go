package provider

// Retiring a built-in subscription. Each subscription with a mover below
// has a community plugin that does what the built-in does
// (github.com/magpie-community/plugins). Moving one puts its accounts onto
// the plugin under the same id, so agents on zed/<model>, the model picks,
// routing and fallbacks carry on as they were:
//
//  1. the plugin is installed, if it isn't;
//  2. each account's sign-in is kept as the plugin keeps one, in the order
//     and on or off as the user had it;
//  3. each is tried as a request would try it (the plugin's loader and its
//     model list for that account), and every model the built-in has in use
//     must be one the plugin serves;
//  4. only then does the id change hands: the built-in's saved accounts go
//     out of logins.json into migrations.json, kept for going back.
//
// Anything short of that puts everything back as it was — the plugin's
// newer tokens included, since a refresh token used once is spent — and
// the built-in carries on. MoveBack undoes a move the same way.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/update"
)

// A migration's states.
const (
	MoveMoving = "moving" // under way, or cut short (put back at the next try)
	MovePlugin = "plugin" // the plugin has the id
	MovedBack  = "back"   // the user moved it back: it stays built-in
	MoveFailed = "failed" // the last try failed: the built-in carries on
)

// Retiring are the built-ins magpie moves onto their plugins by itself,
// at start-up; the rest move only when the user asks.
var Retiring = []string{}

// moveRetry is how long a failed move waits before it is tried again.
const moveRetry = 6 * time.Hour

// Migration is where one built-in's move stands.
type Migration struct {
	State   string    `json:"state"`
	Package string    `json:"package,omitempty"`
	At      time.Time `json:"at"`
	Err     string    `json:"error,omitempty"`
	// Accounts are the plugin's accounts the move made or took over.
	Accounts []movedAccount `json:"accounts,omitempty"`
	// Backup is the built-in's accounts as logins.json kept them.
	Backup []savedLogin `json:"backup,omitempty"`
	// Kept is what else of the built-in's the move set aside (Kiro's key).
	Kept json.RawMessage `json:"kept,omitempty"`
}

type movedAccount struct {
	Key  string `json:"key"`
	User string `json:"user"`
	Own  bool   `json:"own,omitempty"`
	// Was is what the plugin kept at Key before, when the user had signed
	// in to the same account through the plugin already.
	Was map[string]any `json:"was,omitempty"`
	// Sent is (a hash of) the sign-in given to the plugin: a plugin still
	// holding it renewed nothing, so the built-in's own is as new.
	Sent string `json:"sent,omitempty"`
}

// signinHash tells two sign-ins apart without keeping either.
func signinHash(a map[string]any) string {
	h := sha256.Sum256([]byte(jsonText(a)))
	return hex.EncodeToString(h[:8])
}

// renewedSince are the built-in's accounts whose sign-in isn't the one
// sent: the built-in renewed them while the move went on (a request, a
// refresh) and, a refresh token being spent once used, the plugin's copy
// may be dead.
func renewedSince(mv *mover, moved []movedAccount) []string {
	now, err := mv.out()
	if err != nil {
		return nil
	}
	var out []string
	for _, ma := range moved {
		if ma.Own {
			continue
		}
		for _, a := range now {
			if strings.EqualFold(a.User, ma.User) && signinHash(a.Auth) != ma.Sent {
				out = append(out, ma.User)
			}
		}
	}
	return out
}

// Moving is one of a built-in's accounts on its way to the plugin.
type Moving struct {
	User      string
	First, On bool
	// Lapsed is an account the vendor already refused: it goes along, but
	// isn't tried.
	Lapsed bool
	// Own is the agent's own sign-in (its CLI's, its app's), which the
	// plugin reads where the agent keeps it, as the built-in did.
	Own  bool
	Auth map[string]any
}

// A mover carries one built-in's accounts to its plugin and back.
type mover struct {
	pkg string
	// min is the oldest version of pkg that runs the accounts as moved
	min string
	// agents are the logins.json agents its accounts are saved under.
	agents []string
	// out is the built-in's accounts as the plugin's sign-ins, the one in
	// use first.
	out func() ([]Moving, error)
	// back puts one of the plugin's sign-ins back into the built-in's
	// saved accounts: into the one it came from (user), else a new one.
	// It gives the accounts and the one it wrote to.
	back func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error)
	// take and give set aside what else of the built-in's the plugin now
	// has, and put it back.
	take func() (json.RawMessage, error)
	give func(json.RawMessage) error
}

var movers = map[string]*mover{}

// errStays is a back's answer for a sign-in the built-in has nowhere to
// keep (one made in the plugin's own way): it stays with the plugin, which
// lists it under its own id once the built-in has the id again.
var errStays = errors.New("stays with the plugin")

// Movable is whether the built-in id has a plugin it can move to.
func Movable(id string) bool { return movers[id] != nil }

// MovePackage is the plugin the built-in id moves to.
func MovePackage(id string) string {
	if m := movers[id]; m != nil {
		return m.pkg
	}
	return ""
}

func migrationsPath() string { return filepath.Join(filepath.Dir(Path()), "migrations.json") }

var migrationsMu sync.Mutex

func readMigrations() map[string]Migration {
	m, _ := filememo.Read("migrations", migrationsPath(), func(b []byte) (map[string]Migration, error) {
		var m map[string]Migration
		_ = json.Unmarshal(b, &m)
		return m, nil
	})
	return m
}

// MigrationOf is where the built-in id's move stands.
func MigrationOf(id string) (Migration, bool) {
	m, ok := readMigrations()[id]
	return m, ok
}

// Moved is whether the built-in id's accounts are its plugin's now.
func Moved(id string) bool {
	m, ok := MigrationOf(id)
	return ok && m.State == MovePlugin
}

// OnPlugins are the built-ins moved onto their plugins.
func OnPlugins() []string {
	var out []string
	for id, m := range readMigrations() {
		if m.State == MovePlugin && movers[id] != nil {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func movingNow(id string) bool {
	m, ok := MigrationOf(id)
	return ok && m.State == MoveMoving
}

func setMigration(id string, f func(m *Migration)) error {
	migrationsMu.Lock()
	defer migrationsMu.Unlock()
	all := map[string]Migration{}
	for k, v := range readMigrations() {
		all[k] = v
	}
	m := all[id]
	f(&m)
	all[id] = m
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(migrationsPath(), append(b, '\n'))
}

// lockMoves keeps two magpies (the app and a CLI, a dev build beside a
// release) from moving at once.
func lockMoves() (func(), error) {
	p := migrationsPath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	for range 2 {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if fi, serr := os.Stat(p); serr == nil && time.Since(fi.ModTime()) > 15*time.Minute {
			os.Remove(p) // left by a magpie that died moving
			continue
		}
		return nil, errors.New("another magpie is moving subscriptions to plugins")
	}
	return nil, errors.New("another magpie is moving subscriptions to plugins")
}

// Hooks the tests stand in for.
var (
	installPlugin = func(ctx context.Context, pkg, min string) error {
		for _, e := range plugin.Load().Plugins {
			if plugin.PackageName(e.Spec) != pkg {
				continue
			}
			if e.Off {
				return fmt.Errorf("%s is turned off in Plugins", pkg)
			}
			v := plugin.Version(e.Spec)
			if min == "" || !update.Newer(min, v) {
				return nil
			}
			if plugin.IsPath(e.Spec) {
				return fmt.Errorf("%s at %s is %s; moving needs %s or newer", pkg, e.Spec, v, min)
			}
			break
		}
		_, err := plugin.Add(ctx, pkg)
		if err == nil && min != "" {
			if v := plugin.Version(pkg); update.Newer(min, v) {
				return fmt.Errorf("%s %s is installed; moving needs %s or newer", pkg, v, min)
			}
		}
		return err
	}
	pluginProviders = plugin.Providers
)

// modelsInUse are the models the built-in id serves now: the ones agents,
// picks and routing can name.
var modelsInUse = func(id string) []string {
	for _, p := range All() {
		if p.ID == id && !p.IsPlugin() {
			var out []string
			for _, m := range p.Exposed() {
				out = append(out, m.ID)
			}
			return out
		}
	}
	return nil
}

// Move puts the built-in id's accounts onto its plugin (see above). An
// error leaves the built-in as it was.
func Move(ctx context.Context, id string) error {
	mv := movers[id]
	if mv == nil {
		return fmt.Errorf("%s has no plugin to move to", id)
	}
	unlock, err := lockMoves()
	if err != nil {
		return err
	}
	defer unlock()
	if Moved(id) {
		return nil
	}
	if movingNow(id) {
		putBack(ctx, id, mv, "")
	}
	err = move(ctx, id, mv)
	if err != nil {
		_ = setMigration(id, func(m *Migration) {
			*m = Migration{State: MoveFailed, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second), Err: err.Error()}
		})
	}
	return err
}

func move(ctx context.Context, id string, mv *mover) (err error) {
	inUse := modelsInUse(id)
	accts, err := mv.out()
	if err != nil {
		return err
	}
	if len(accts) == 0 {
		return fmt.Errorf("no %s account to move", id)
	}
	if err := installPlugin(ctx, mv.pkg, mv.min); err != nil {
		return fmt.Errorf("installing %s: %w", mv.pkg, err)
	}
	pps, err := pluginProviders(ctx)
	if err != nil {
		return err
	}
	var pp plugin.Provider
	for _, p := range pps {
		if p.ID == id {
			if plugin.PackageName(p.Spec) != mv.pkg {
				return fmt.Errorf("%s is served by another plugin, %s", id, p.Spec)
			}
			pp = p
		}
	}
	if pp.ID == "" {
		return fmt.Errorf("%s doesn't serve %s", mv.pkg, id)
	}
	before := plugin.Auths(id)
	if err := setMigration(id, func(m *Migration) {
		*m = Migration{State: MoveMoving, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second)}
	}); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			putBack(ctx, id, mv, err.Error())
		}
	}()
	var moved []movedAccount
	for _, a := range accts {
		key, err := plugin.Import(ctx, id, a.Auth)
		if err != nil {
			return fmt.Errorf("%s: %w", a.User, err)
		}
		ma := movedAccount{Key: key, User: a.User, Own: a.Own, Was: before[key], Sent: signinHash(a.Auth)}
		moved = append(moved, ma)
		if err := setMigration(id, func(m *Migration) { m.Accounts = moved }); err != nil {
			return err
		}
	}
	if err := keepOrder(pp, accts, moved); err != nil {
		return err
	}
	tried := false
	for i, a := range accts {
		if a.Lapsed {
			continue
		}
		models, err := plugin.Check(ctx, id, moved[i].Key)
		if err != nil {
			return fmt.Errorf("%s through the plugin: %w", a.User, err)
		}
		if !tried {
			if missing := slices.DeleteFunc(slices.Clone(inUse), func(m string) bool { return slices.Contains(models, m) }); len(missing) > 0 {
				return fmt.Errorf("the plugin doesn't serve %s", strings.Join(missing, ", "))
			}
			tried = true
		}
	}
	if !tried {
		return fmt.Errorf("every %s account needs signing in again", id)
	}
	return commitMove(id, mv, moved)
}

// keepOrder lists the accounts moved as the built-in had them: the one in
// use first, the rest on or off. What the user had signed in to through
// the plugin already keeps its place.
func keepOrder(pp plugin.Provider, accts []Moving, moved []movedAccount) error {
	agent := pluginAgent(pp)
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	first := ""
	for i, a := range accts {
		if a.First && moved[i].Was == nil {
			first = moved[i].Key
		}
	}
	for i, a := range accts {
		ma := moved[i]
		if ma.Was != nil {
			continue
		}
		ls = slices.DeleteFunc(ls, func(l savedLogin) bool { return l.Agent == agent && l.Home == ma.Key })
		ls = append(ls, savedLogin{Agent: agent, User: a.User, Home: ma.Key, On: a.On || a.First, First: ma.Key == first, Seen: time.Now().UTC().Truncate(time.Second)})
	}
	if first != "" {
		for i := range ls {
			if ls[i].Agent == agent && ls[i].Home != first {
				ls[i].First = false
			}
		}
	}
	return writeLogins(ls)
}

// commitMove hands the id over: the built-in's accounts go aside.
func commitMove(id string, mv *mover, moved []movedAccount) error {
	if r := renewedSince(mv, moved); len(r) > 0 {
		return fmt.Errorf("%s renewed while moving; tried again later", strings.Join(r, ", "))
	}
	var kept json.RawMessage
	if mv.take != nil {
		k, err := mv.take()
		if err != nil {
			return err
		}
		kept = k
	}
	loginsMu.Lock()
	var backup []savedLogin
	for _, l := range readLogins() {
		if slices.Contains(mv.agents, l.Agent) {
			backup = append(backup, l)
		}
	}
	loginsMu.Unlock()
	if err := setMigration(id, func(m *Migration) {
		*m = Migration{State: MovePlugin, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second), Accounts: moved, Backup: backup, Kept: kept}
	}); err != nil {
		if mv.give != nil {
			_ = mv.give(kept)
		}
		return err
	}
	tidyMoved(id)
	return nil
}

// tidyMoved takes a moved built-in's accounts out of logins.json (into its
// backup, if one was left behind when a magpie stopped between the two).
func tidyMoved(id string) {
	mv := movers[id]
	if mv == nil || !Moved(id) {
		return
	}
	loginsMu.Lock()
	ls := readLogins()
	var left []savedLogin
	ls = slices.DeleteFunc(ls, func(l savedLogin) bool {
		if slices.Contains(mv.agents, l.Agent) {
			left = append(left, l)
			return true
		}
		return false
	})
	if len(left) > 0 {
		_ = writeLogins(ls)
	}
	loginsMu.Unlock()
	if len(left) == 0 {
		return
	}
	_ = setMigration(id, func(m *Migration) {
		for _, l := range left {
			if !slices.ContainsFunc(m.Backup, func(b savedLogin) bool { return sameMoved(b, l) }) {
				m.Backup = append(m.Backup, l)
			}
		}
	})
}

func sameMoved(a, b savedLogin) bool {
	return a.Agent == b.Agent && strings.EqualFold(a.User, b.User) && a.Home == b.Home
}

// putBack undoes a move cut short: the plugin's sign-ins go back into the
// built-in's accounts (its newer tokens with them), then out of the
// plugin — but for one the user had there already, which gets back what
// it had.
func putBack(ctx context.Context, id string, mv *mover, why string) {
	m, _ := MigrationOf(id)
	var keys []string
	for _, ma := range m.Accounts {
		keys = append(keys, ma.Key)
	}
	renewed := renewedSince(mv, m.Accounts)
	skip := func(ma movedAccount, a map[string]any) bool {
		return signinHash(a) == ma.Sent || slices.ContainsFunc(renewed, func(u string) bool { return strings.EqualFold(u, ma.User) })
	}
	_, _, _ = handBack(ctx, id, mv, m.Accounts, keys, skip, nil, func(ls []savedLogin, _ map[string]string) []savedLogin { return ls })
	for _, ma := range m.Accounts {
		if ma.Was != nil {
			_ = plugin.Restore(ctx, id, ma.Key, ma.Was)
		}
	}
	_ = setMigration(id, func(m *Migration) {
		*m = Migration{State: MoveFailed, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second), Err: why}
	})
}

// handBack writes the plugin's accounts keys back into the built-in's
// saved accounts, then takes them out of the plugin. They are written
// before they are taken, so a magpie stopped between the two loses no
// sign-in; one the plugin renewed in between is written again. arrange
// finishes the accounts written (given each key's user). An account that
// can't go back stops it all before anything is written.
func handBack(ctx context.Context, id string, mv *mover, accts []movedAccount, keys []string, skip func(movedAccount, map[string]any) bool, prep, arrange func([]savedLogin, map[string]string) []savedLogin) (map[string]string, []error, error) {
	var stays []string
	byKey := map[string]movedAccount{}
	for _, ma := range accts {
		byKey[ma.Key] = ma
	}
	write := func(auths map[string]map[string]any) (map[string]string, []error, error) {
		loginsMu.Lock()
		defer loginsMu.Unlock()
		ls := readLogins()
		if prep != nil {
			ls = prep(ls, nil)
		}
		users := map[string]string{}
		var errs []error
		stays = nil
		for _, k := range keys {
			ma := byKey[k]
			if ma.Own {
				users[k] = ma.User
				continue
			}
			a := auths[k]
			if a == nil {
				continue
			}
			if skip != nil && skip(ma, a) {
				users[k] = ma.User
				continue
			}
			next, u, err := mv.back(ls, ma.User, a)
			if errors.Is(err, errStays) {
				stays = append(stays, k)
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			ls, users[k] = next, u
		}
		if len(errs) > 0 {
			return users, errs, nil
		}
		return users, nil, writeLogins(arrange(ls, users))
	}
	seen := plugin.Auths(id)
	users, errs, err := write(seen)
	if err != nil || len(errs) > 0 {
		return users, errs, err
	}
	keys = slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return slices.Contains(stays, k) })
	if len(keys) == 0 {
		return users, nil, nil
	}
	taken, err := plugin.Take(ctx, id, keys)
	if err != nil {
		return users, nil, err
	}
	for _, k := range keys {
		if t := taken[k]; t != nil && jsonText(t) != jsonText(seen[k]) {
			return write(taken)
		}
	}
	return users, nil, nil
}

// MoveBack gives the id back to the built-in: its accounts as they were
// set aside, with what the plugin has of them now (newer tokens, accounts
// signed in to since), in the plugin's order; then the plugin signs out.
func MoveBack(ctx context.Context, id string) error {
	mv := movers[id]
	if mv == nil {
		return fmt.Errorf("%s isn't a built-in subscription", id)
	}
	unlock, err := lockMoves()
	if err != nil {
		return err
	}
	defer unlock()
	m, ok := MigrationOf(id)
	if !ok || m.State != MovePlugin {
		return nil
	}
	var order []plugin.Account
	if pp, ok := pluginOfAgent("plugin:" + id); ok {
		for _, l := range pluginLogins(pp) {
			order = append(order, l.acct)
		}
	}
	keys := slices.Collect(maps.Keys(plugin.Auths(id)))
	sort.SliceStable(keys, func(i, j int) bool {
		if a, b := indexOfKey(order, keys[i]), indexOfKey(order, keys[j]); a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	onOf := pluginOn(id)
	// the backup first, for back to write into the accounts it came from
	prep := func(ls []savedLogin, _ map[string]string) []savedLogin {
		for _, b := range m.Backup {
			if !slices.ContainsFunc(ls, func(l savedLogin) bool { return sameMoved(l, b) }) {
				ls = append(ls, b)
			}
		}
		return ls
	}
	arrange := func(ls []savedLogin, users map[string]string) []savedLogin {
		// the plugin's rows of the accounts going back go with them, or
		// the accounts page lists each twice, the plugin's copy empty
		ls = slices.DeleteFunc(ls, func(l savedLogin) bool {
			_, back := users[l.Home]
			return l.Agent == "plugin:"+id && (back || len(keys) == 0)
		})
		firstUser := ""
		for i, k := range keys {
			user := users[k]
			if user == "" {
				continue
			}
			if firstUser == "" && i == 0 {
				firstUser = user
			}
			for j := range ls {
				if slices.Contains(mv.agents, ls[j].Agent) && strings.EqualFold(ls[j].User, user) {
					if on, ok := onOf[k]; ok {
						ls[j].On = on
					}
				}
			}
		}
		if firstUser != "" {
			for j := range ls {
				if slices.Contains(mv.agents, ls[j].Agent) {
					ls[j].First = strings.EqualFold(ls[j].User, firstUser) && !ls[j].own()
				}
			}
		}
		return ls
	}
	// the backup goes back even with no account left in the plugin
	if len(keys) == 0 {
		loginsMu.Lock()
		err := writeLogins(arrange(prep(readLogins(), nil), nil))
		loginsMu.Unlock()
		if err != nil {
			return err
		}
	} else {
		_, errs, err := handBack(ctx, id, mv, m.Accounts, keys, nil, prep, arrange)
		if err != nil {
			tidyMoved(id)
			return err
		}
		if len(errs) > 0 {
			// the plugin keeps the id and every account
			return errors.Join(errs...)
		}
	}
	if mv.give != nil && len(m.Kept) > 0 {
		if err := mv.give(m.Kept); err != nil {
			return err
		}
	}
	return setMigration(id, func(m *Migration) {
		*m = Migration{State: MovedBack, Package: mv.pkg, At: time.Now().UTC().Truncate(time.Second)}
	})
}

func indexOfKey(order []plugin.Account, key string) int {
	if i := slices.IndexFunc(order, func(a plugin.Account) bool { return a.Key == key }); i >= 0 {
		return i
	}
	return len(order)
}

// pluginOn is which of the plugin provider id's accounts are on.
func pluginOn(id string) map[string]bool {
	out := map[string]bool{}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == "plugin:"+id {
			out[l.Home] = l.On || l.First
		}
	}
	return out
}

// MoveRetiring moves the built-ins being retired that have accounts, at
// start-up: once, or again moveRetry after one failed. One the user moved
// back stays.
func MoveRetiring(ctx context.Context) map[string]error {
	out := map[string]error{}
	for _, id := range Retiring {
		m, ok := MigrationOf(id)
		switch {
		case ok && m.State == MovePlugin:
			tidyMoved(id)
			continue
		case ok && m.State == MovedBack:
			continue
		case ok && m.State == MoveFailed && time.Since(m.At) < moveRetry:
			continue
		}
		if accts, err := movers[id].out(); err != nil || len(accts) == 0 {
			continue
		}
		out[id] = Move(ctx, id)
	}
	return out
}

// KeepRetiringMoved moves the built-ins being retired, run by the magpie
// serving the gateway (one magpie, never two at once): a little after it
// starts, then every hour, so a failed move is tried again once moveRetry
// has gone.
func KeepRetiringMoved(ctx context.Context) {
	if len(Retiring) == 0 {
		return
	}
	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for id, err := range MoveRetiring(ctx) {
			if err != nil {
				log.Printf("moving %s to its plugin: %s (it stays built-in)", id, err)
			} else {
				log.Printf("moved %s to its plugin, %s", id, MovePackage(id))
			}
		}
		t.Reset(time.Hour)
	}
}
