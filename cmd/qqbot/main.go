// Command qqbot is a QQ group monitor that links group members to new-api
// accounts: it mirrors rosters and sign-in boards, binds members by their QQ
// email, promotes accounts into an authorised group, and pays a daily
// check-in reward.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"qqbot/internal/api"
	"qqbot/internal/config"
	"qqbot/internal/events"
	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/scheduler"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

func main() {
	var (
		addr    = flag.String("addr", ":4096", "HTTP listen address for the WebUI")
		dataDir = flag.String("data", "data", "data directory (SQLite database and config.yaml)")
		webDist = flag.String("web", "web/dist", "directory holding the built WebUI")
	)
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// The environment (.env in the data directory, or the process environment)
	// is read first: it carries both the admin credential and the listen port.
	// Reading it before the server starts means a fresh instance is never
	// briefly open to whoever reaches it first.
	env, err := config.LoadEnv(*dataDir)
	if err != nil {
		log.Printf("warning: could not read the env file: %v", err)
	}

	// Precedence for the listen address: an explicit -addr flag wins, then
	// QQBOT_PORT from the environment, then the flag's default. The flag is
	// checked for having been set rather than compared to its default, so a
	// deliberate "-addr :4096" still overrides a configured port.
	explicitAddr := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			explicitAddr = true
		}
	})
	if !explicitAddr {
		if p := config.EnvPortFromEnv(env); p != "" {
			*addr = p
		}
	}

	st, err := store.Open(filepath.Join(*dataDir, "qqbot.db"))
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer st.Close()

	// Connection settings live in the data directory as well, so the whole
	// instance is one directory to back up or move.
	cfgMgr := config.NewManager(filepath.Join(*dataDir, config.FileName), st)
	if err := cfgMgr.EnsureFile(); err != nil {
		log.Printf("warning: could not create %s: %v", cfgMgr.Path(), err)
	}
	log.Printf("data directory: %s (database: qqbot.db, config: %s)", *dataDir, config.FileName)

	// The gateway and NapCat clients are rebuilt per call from the stored
	// configuration, so editing either in the WebUI takes effect immediately
	// without restarting the service.
	gw := &lazyGateway{cfg: cfgMgr, lockout: newapi.NewLockout(nil)}
	qq := &lazyQQ{cfg: cfgMgr}

	svc := service.New(st, gw, qq)
	sched := scheduler.New(svc)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go sched.Run(ctx)

	// The event listener only shortens reaction time (binding commands); the
	// sweeps above remain the source of truth, so a missing event stream is
	// harmless. It blocks until shutdown, so it must run in its own goroutine.
	go events.New(func() (string, string) {
		c := cfgMgr.MustLoad()
		return c.NapCat.EventWSURL, c.NapCat.EventWSToken
	}, bindAdapter{svc}).Run(ctx)

	router := api.NewRouter(st, svc, cfgMgr, *webDist)
	router.SetNapCatAdmin(napcatAdmin{})
	router.SetProber(prober{gw: gw})
	// The scheduler's health is what the panel's status view reads, so the
	// patrols it has actually run are visible from the UI.
	router.SetPatrolStatus(sched)
	// The patrol card's 测试一次 button runs a sweep through the same scheduler,
	// so a manual run and a timed run cannot behave differently.
	router.SetPatrolRunner(sched)

	// The admin credential was read from the environment above. Provisioning it
	// before the server accepts requests means a fresh instance is never
	// briefly open to whoever reaches it first; without it, the WebUI's
	// first-run form creates the admin as before.
	if creds := config.AdminCredsFromEnv(env); creds.Set {
		if err := router.EnsureAdminFromEnv(creds); err != nil {
			log.Fatalf("configure admin from %s: %v", config.EnvAdminPassword, err)
		}
		log.Printf("admin %q configured from the environment", creds.Username)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           router.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("qqbot listening on %s (data: %s)", *addr, *dataDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// lazyGateway builds a new-api client from stored configuration and reuses it
// until the configuration changes.
//
// Caching matters for more than speed: the gateway caps concurrent login
// sessions (it answers 409 AUTH_SESSION_LIMIT), so logging in on every call
// breaks as soon as a sweep makes more than a handful of calls.
type lazyGateway struct {
	cfg *config.Manager

	// lockout remembers a rate-limited login. Without it every patrol tick and
	// every UI action retries, each rejection counts against the gateway's
	// limiter, and the window never closes (see newapi.Lockout).
	lockout *newapi.Lockout

	mu     sync.Mutex
	cached *newapi.Client
	key    string // config fingerprint the cached client was built from
}

// client returns a logged-in client, re-authenticating only when needed.
//
// The client is built once and then kept, including across access-token expiry.
// That is not an optimisation: the session cookie lives in the client's jar, so
// rebuilding it throws the session away and the next call has to log in again —
// which is exactly what filled new-api's session list with one entry per call.
func (l *lazyGateway) client() (*newapi.Client, error) {
	cfg := l.cfg.MustLoad()
	if cfg.NewAPI.URL == "" {
		return nil, errNotConfigured
	}
	key := cfg.NewAPI.URL + "\x00" + cfg.NewAPI.Username + "\x00" + cfg.NewAPI.Password

	l.mu.Lock()
	defer l.mu.Unlock()

	// A usable token needs no work at all.
	if l.cached != nil && l.key == key && l.cached.TokenFresh() {
		return l.cached, nil
	}

	// While the gateway is rate-limiting us, no attempt is made at all: the
	// operator gets the remaining wait instead of another rejection, and the
	// gateway's budget stops being spent by our own retries.
	if l.lockout != nil {
		if err := l.lockout.Blocked(); err != nil {
			return nil, err
		}
	}

	// Reuse the existing client when the settings have not changed. Its jar
	// still holds the session, so reauthentication refreshes that session rather
	// than creating another one.
	c := l.cached
	if c == nil || l.key != key {
		c = newapi.New(cfg.NewAPI.URL)
		// A restart has no jar to inherit, so the session from the last run is
		// restored from storage. Without it every start would log in and add a
		// session to the account's list. It is looked up under this host and
		// account: a session issued elsewhere is not this client's credential,
		// and importing it would send it to a host that never issued it.
		origin := config.SessionOrigin(cfg.NewAPI.URL, cfg.NewAPI.Username, cfg.NewAPI.Password)
		if saved := l.cfg.LoadNewAPISession(origin); saved != "" {
			if err := c.ImportSession(saved); err != nil {
				// A malformed value is not worth keeping; a well-formed one that
				// is dead fails at the refresh below, which is where we find out.
				_ = l.cfg.ClearNewAPISession()
			}
		}
	}

	// Reauthenticate, not Login: it refreshes the existing session when it can,
	// and only logs in when there is nothing to refresh. The difference matters
	// — the access token lives 15 minutes while a login session lives 30 days,
	// so re-logging-in every time the token expires adds a session every 15
	// minutes until the account hits its cap and nobody can log in.
	if err := c.Reauthenticate(cfg.NewAPI.Username, cfg.NewAPI.Password); err != nil {
		if l.lockout != nil {
			l.lockout.Fail(err)
		}
		// A rejected session must be forgotten, or every later attempt would
		// send the same dead credential instead of logging in.
		if newapi.IsAuthFailure(err) {
			_ = l.cfg.ClearNewAPISession()
			// Drop it from the cache too: the next call must start clean rather
			// than keep retrying the dead session.
			l.cached, l.key = nil, ""
		}
		return nil, err
	}
	// Remember the (possibly rotated) session for the next start, under the
	// origin it was just issued for.
	if s := c.ExportSession(); s != "" {
		_ = l.cfg.SaveNewAPISession(config.SessionOrigin(cfg.NewAPI.URL, cfg.NewAPI.Username, cfg.NewAPI.Password), s)
	}
	if l.lockout != nil {
		l.lockout.Success()
	}
	l.cached, l.key = c, key
	return c, nil
}

// matches reports whether these credentials are the ones the live gateway is
// configured with, i.e. whether its session can answer for them.
//
// Both sides are normalised the same way: the stored URL keeps whatever the
// operator typed (a trailing slash is valid and stored verbatim), so comparing
// it against a trimmed argument would fail for the identical address and make
// 测试连接 perform a real login — a new gateway session and a login-budget
// spend — for a configuration that never changed.
func (l *lazyGateway) matches(url, username, password string) bool {
	cfg := l.cfg.MustLoad()
	return strings.TrimRight(cfg.NewAPI.URL, "/") == strings.TrimRight(url, "/") &&
		cfg.NewAPI.Username == username &&
		cfg.NewAPI.Password == password
}

// invalidateAfter drops the cached client when a request shows the session is
// no longer usable.
//
// It is deliberately narrow: only an auth failure justifies a re-login. Doing it
// on every error — as this used to — turns one rate limit or one permission
// rejection into a fresh login attempt, spending the gateway's login budget
// until the service is locked out (see newapi.IsAuthFailure).
func (l *lazyGateway) invalidateAfter(err error) {
	if !newapi.IsAuthFailure(err) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cached = nil
	l.key = ""
}

func (l *lazyGateway) FindUserByEmail(addr string) (*newapi.User, error) {
	c, err := l.client()
	if err != nil {
		return nil, err
	}
	u, err := c.FindUserByEmail(addr)
	l.invalidateAfter(err)
	return u, err
}

func (l *lazyGateway) GetUser(id int) (*newapi.User, error) {
	c, err := l.client()
	if err != nil {
		return nil, err
	}
	u, err := c.GetUser(id)
	l.invalidateAfter(err)
	return u, err
}

func (l *lazyGateway) SetGroup(u *newapi.User, group string) error {
	c, err := l.client()
	if err != nil {
		return err
	}
	if err := c.SetGroup(u, group); err != nil {
		l.invalidateAfter(err)
		return err
	}
	return nil
}

func (l *lazyGateway) Groups() (map[string]struct{}, error) {
	c, err := l.client()
	if err != nil {
		return nil, err
	}
	g, err := c.Groups()
	l.invalidateAfter(err)
	return g, err
}

// ListUsers returns every account on the instance, for the user directory.
func (l *lazyGateway) ListUsers() ([]newapi.User, error) {
	c, err := l.client()
	if err != nil {
		return nil, err
	}
	us, err := c.ListUsers()
	l.invalidateAfter(err)
	return us, err
}

func (l *lazyGateway) AddQuota(userID, value int) error {
	c, err := l.client()
	if err != nil {
		return err
	}
	if err := c.AddQuota(userID, value); err != nil {
		l.invalidateAfter(err)
		return err
	}
	return nil
}

// lazyQQ prefers the OneBot HTTP endpoint and falls back to the WebUI bridge,
// which needs no network adapter to be enabled.
type lazyQQ struct{ cfg *config.Manager }

func (l *lazyQQ) onebot() (*napcat.Client, error) {
	cfg := l.cfg.MustLoad()
	if cfg.NapCat.OneBotURL == "" {
		return nil, errNotConfigured
	}
	return napcat.New(cfg.NapCat.OneBotURL, cfg.NapCat.OneBotToken), nil
}

func (l *lazyQQ) webui() (*napcat.WebUI, error) {
	cfg := l.cfg.MustLoad()
	if cfg.NapCat.WebUIURL == "" {
		return nil, errNotConfigured
	}
	w := napcat.NewWebUI(cfg.NapCat.WebUIURL, cfg.NapCat.WebUIToken)
	if err := w.Login(); err != nil {
		return nil, err
	}
	return w, nil
}

func (l *lazyQQ) GetGroupList() ([]napcat.Group, error) {
	if c, err := l.onebot(); err == nil {
		return c.GetGroupList()
	}
	w, err := l.webui()
	if err != nil {
		return nil, err
	}
	return w.GetGroupList()
}

func (l *lazyQQ) GetGroupMemberList(groupID int64) ([]napcat.GroupMember, error) {
	if c, err := l.onebot(); err == nil {
		return c.GetGroupMemberList(groupID)
	}
	w, err := l.webui()
	if err != nil {
		return nil, err
	}
	return w.GetGroupMemberList(groupID)
}

func (l *lazyQQ) GetGroupSignedList(groupID int64) ([]napcat.SignedMember, error) {
	if c, err := l.onebot(); err == nil {
		return c.GetGroupSignedList(groupID)
	}
	w, err := l.webui()
	if err != nil {
		return nil, err
	}
	return w.GetGroupSignedList(groupID)
}

// napcatAdmin logs in to a NapCat WebUI and writes its OneBot adapters, which
// is what the connection page's "auto-configure" button drives.
type napcatAdmin struct{}

// EnsureAdapters logs in with the supplied WebUI credential and provisions the
// HTTP + WebSocket adapters. It is stateless: the credential comes from the
// request, so nothing is cached that could go stale after a NapCat restart
// (whose WebUI credential is signed with a per-process secret).
func (napcatAdmin) EnsureAdapters(webuiURL, webuiToken string, opts napcat.AdapterOptions) (*napcat.AdapterResult, error) {
	w := napcat.NewWebUI(webuiURL, webuiToken)
	if err := w.Login(); err != nil {
		return nil, err
	}
	return w.EnsureAdapters(opts)
}

// prober reaches the two external systems for the connection tests.
//
// The new-api probe goes through the live gateway when the credentials it is
// given are the ones already in use: a session proves those credentials are
// valid, so logging in again would only add another entry to the account's
// session list (and spend the login rate limit) to re-learn something the
// service already knows. Changed credentials cannot be checked that way — a
// session belongs to the old account — so those do log in.
type prober struct {
	// gw is the live gateway. Optional: without it every probe logs in, which is
	// what a test env gets.
	gw *lazyGateway
}

// ProbeGateway reports the account behind the credentials.
func (p prober) ProbeGateway(url, username, password string) (*newapi.User, bool, error) {
	if p.gw != nil && p.gw.matches(url, username, password) {
		c, err := p.gw.client()
		if err == nil {
			u, err := c.Self()
			if err == nil {
				return u, true, nil
			}
		}
		// Fall through to a real login: the live session could not answer, and
		// the operator asked for a test, so they get one.
	}
	c := newapi.New(url)
	if err := c.Login(username, password); err != nil {
		return nil, false, err
	}
	u, err := c.Self()
	return u, false, err
}

// ProbeWebUI logs in to NapCat's panel and asks the debug bridge for the bot
// identity. The bridge needs QQ to be logged in; when it is not, the login
// itself still proves the panel and token are right, so that is reported as a
// partial success rather than a failure.
func (prober) ProbeWebUI(webuiURL, webuiToken string) (*napcat.LoginInfo, error) {
	w := napcat.NewWebUI(webuiURL, webuiToken)
	if err := w.Login(); err != nil {
		return nil, err
	}
	info, err := w.GetLoginInfo()
	if err != nil {
		// The panel accepted the credential but QQ is not logged in. Say so
		// explicitly: "not logged in" is a different problem from "wrong token".
		return nil, nil
	}
	return info, nil
}

// ProbeOneBot asks an OneBot HTTP endpoint for the bot identity.
func (prober) ProbeOneBot(onebotURL, onebotToken string) (*napcat.LoginInfo, error) {
	return napcat.New(onebotURL, onebotToken).GetLoginInfo()
}

// ProbeEventWS opens the event stream just long enough to prove it accepts us.
func (prober) ProbeEventWS(wsURL, token string) error {
	return events.Probe(wsURL, token)
}

// bindAdapter narrows the service to the methods the event listener needs.
//
// The switches and the command patterns are read through the service on every
// event rather than cached here, so an operator's change in the WebUI takes
// effect on the next message with no restart.
type bindAdapter struct{ svc *service.Service }

func (b bindAdapter) IsMonitoredGroup(groupID int64) (bool, error) {
	return b.svc.IsMonitoredGroup(groupID)
}

func (b bindAdapter) BindByCommand(groupID, qq int64) error {
	_, err := b.svc.BindByCommand(groupID, qq)
	return err
}

func (b bindAdapter) BindMember(groupID, qq int64) error {
	return b.svc.BindMember(groupID, qq)
}

func (b bindAdapter) IsBindCommand(message string) bool {
	return b.svc.IsBindCommand(message)
}

func (b bindAdapter) IsCheckinCommand(message string) bool {
	return b.svc.IsCheckinCommand(message)
}

func (b bindAdapter) CheckinByAnyMessage() bool {
	return b.svc.CheckinByAnyMessage()
}

func (b bindAdapter) BindOnJoinEnabled() bool {
	return b.svc.BindOnJoinEnabled()
}

func (b bindAdapter) CheckinByMessage(groupID, qq int64) (events.CheckinOutcome, error) {
	res, err := b.svc.CheckinByMessage(groupID, qq)
	return events.CheckinOutcome{
		Recorded:         res.Recorded,
		AlreadyCheckedIn: res.AlreadyCheckedIn,
		Unbound:          res.Unbound,
	}, err
}

func (b bindAdapter) EnsureMember(groupID, qq int64, nickname string) error {
	return b.svc.EnsureMember(groupID, qq, nickname)
}

var errNotConfigured = &notConfiguredError{}

type notConfiguredError struct{}

func (*notConfiguredError) Error() string {
	return "not configured yet: set the new-api and NapCat connection details in the WebUI"
}
