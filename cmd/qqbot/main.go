// Command qqbot is a QQ group monitor that links group members to new-api
// accounts: it mirrors rosters and sign-in boards, binds members by their QQ
// email, promotes accounts into an authorised group, and pays a daily
// check-in reward.
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
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
	"qqbot/internal/logging"
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

	// The log is opened before anything else can fail, and installed as the
	// process default, so every record from here on - including the standard
	// library's and any dependency's - lands in the daily file as well as on
	// stderr. Nothing reads the operator's level yet: the settings live in the
	// database, which is opened a few lines below.
	logs, err := logging.Open(logging.Options{Dir: filepath.Join(*dataDir, logging.DirName)})
	if err != nil {
		log.Fatalf("open log directory: %v", err)
	}
	defer logs.Close()
	logs.InstallDefault()

	// The environment (.env in the data directory, or the process environment)
	// is read first: it carries both the admin credential and the listen port.
	// Reading it before the server starts means a fresh instance is never
	// briefly open to whoever reaches it first.
	env, err := config.LoadEnv(*dataDir)
	if err != nil {
		slog.Error("读取环境文件失败，拒绝启动", "err", err)
		os.Exit(1)
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
		slog.Error("打开数据库失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Connection settings live in the data directory as well, so the whole
	// instance is one directory to back up or move.
	cfgMgr := config.NewManager(filepath.Join(*dataDir, config.FileName), st)
	if err := cfgMgr.EnsureFile(); err != nil {
		slog.Warn("无法创建配置文件", "path", cfgMgr.Path(), "err", err)
	}
	slog.Info("数据目录", "dir", *dataDir, "database", "qqbot.db", "config", config.FileName)

	// The gateway and NapCat clients are rebuilt per call from the stored
	// configuration, so editing either in the WebUI takes effect immediately
	// without restarting the service.
	gw := &lazyGateway{cfg: cfgMgr, lockout: newapi.NewLockout(nil)}
	qq := &lazyQQ{cfg: cfgMgr}

	svc := service.New(st, gw, qq)
	sched := scheduler.New(svc)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Apply the stored log settings and keep doing so: the level is what the
	// operator sets from the panel, and retention is what keeps the directory
	// from growing without bound. A settings read failure leaves the logger at
	// its default rather than failing startup.
	logPolicy := func() logging.Policy {
		s, err := svc.Settings()
		if err != nil {
			return logging.Policy{Level: logging.LevelInfo}
		}
		return logging.Policy{Level: s.LogLevel, RetentionDays: s.LogRetentionDays}
	}
	logs.Apply(logPolicy())

	// The log's calendar is the operator's timezone, the same one that decides
	// what "today" means for check-ins. With the host on UTC and the operator on
	// Asia/Shanghai, using the host's day would file every record written before
	// 08:00 Beijing under the previous day and roll the retention boundary at a
	// time nobody chose. Applied once at start-up: a timezone change is a
	// deliberate reconfiguration, and a restart is already required for the
	// patrol cadence that uses it.
	if s, err := svc.Settings(); err == nil {
		if loc, err := time.LoadLocation(s.Timezone); err == nil {
			logs.SetLocation(loc)
		} else {
			slog.Warn("日志使用系统时区：设置里的时区无法加载", "timezone", s.Timezone, "err", err)
		}
	}

	var workers sync.WaitGroup
	workers.Go(func() { sched.Run(ctx) })
	workers.Go(func() { logs.Maintain(ctx, logPolicy) })

	// The event listener only shortens reaction time (binding commands); the
	// sweeps above remain the source of truth, so a missing event stream is
	// harmless. It blocks until shutdown, so it must run in its own goroutine.
	listener := events.New(func() (string, string) {
		c := cfgMgr.MustLoad()
		return c.NapCat.EventWSURL, c.NapCat.EventWSToken
	}, bindAdapter{svc})
	workers.Go(func() { listener.Run(ctx) })

	router := api.NewRouter(st, svc, cfgMgr, *webDist)
	router.SetNapCatAdmin(napcatAdmin{})
	router.SetProber(prober{gw: gw})
	// The scheduler's health is what the panel's status view reads, so the
	// patrols it has actually run are visible from the UI.
	router.SetPatrolStatus(sched)
	// The patrol card's 测试一次 button runs a sweep through the same scheduler,
	// so a manual run and a timed run cannot behave differently.
	router.SetPatrolRunner(sched)
	// The 日志 page reads these files, and a settings save re-levels through it.
	router.SetLogs(logs)

	// The admin credential was read from the environment above. Provisioning it
	// before the server accepts requests means a fresh instance is never
	// briefly open to whoever reaches it first; without it, the WebUI's
	// first-run form creates the admin as before.
	if creds := config.AdminCredsFromEnv(env); creds.Set {
		if err := router.EnsureAdminFromEnv(creds); err != nil {
			slog.Error("从环境变量配置管理员失败", "env", config.EnvAdminPassword, "err", err)
			os.Exit(1)
		}
		slog.Info("已从环境变量配置管理员", "username", creds.Username)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           router.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("服务已启动", "addr", *addr, "data", *dataDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP 服务退出", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("正在关闭")
	shutdownRuntime(srv, &workers, 10*time.Second)
}

func shutdownRuntime(srv *http.Server, workers *sync.WaitGroup, timeout time.Duration) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	// A sweep that is already in flight still holds the database: Close runs
	// after main returns, so the join has to happen here, not via defer.
	workers.Wait()
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
		// Present one stable identity across restarts. The gateway records the
		// User-Agent on the session row, so this is what makes this service's
		// entry recognisable in the account's session list — and what keeps a
		// restart looking like the same device rather than a new one.
		if secret := l.cfg.SessionFingerprint(); secret != "" {
			c.SetFingerprint(newapi.NewFingerprint(secret))
		}
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
//
// The fallback is driven by napcat.Failover, so it triggers on a *call* failure
// and not only on a construction failure: NapCat's most common fault is an
// expired QQ login, where the OneBot port still listens and answers with an
// error envelope. Building a client for it succeeds every time, so a
// build-time-only fallback would never reach the bridge that still works.
//
// The bridge is cached for the same reason the gateway client is: logging in on
// every call turns a fallback into a fresh problem. A roster read is one call
// per group, so a degraded sweep over 200 groups would perform 200 WebUI logins
// and trip NapCat's own login rate limit — leaving the service with no working
// path at all, which is the opposite of what a fallback is for. The credential
// is also long-lived (signed with the process secret, docs/implementation.md),
// so a single login serves the whole sweep.
type lazyQQ struct {
	cfg *config.Manager

	mu     sync.Mutex
	webui  *napcat.WebUI
	webuiK string // config fingerprint the cached bridge was built from
}

func (l *lazyQQ) onebot() (*napcat.Client, error) {
	cfg := l.cfg.MustLoad()
	if cfg.NapCat.OneBotURL == "" {
		return nil, errNotConfigured
	}
	return napcat.New(cfg.NapCat.OneBotURL, cfg.NapCat.OneBotToken), nil
}

// webuiClient returns a logged-in bridge, re-authenticating when the stored
// address or token changes, or when the cached credential is refused.
func (l *lazyQQ) webuiClient() (*napcat.WebUI, error) {
	cfg := l.cfg.MustLoad()
	if cfg.NapCat.WebUIURL == "" {
		return nil, errNotConfigured
	}
	key := cfg.NapCat.WebUIURL + "\x00" + cfg.NapCat.WebUIToken

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.webui != nil && l.webuiK == key && l.webui.Credential() != "" {
		return l.webui, nil
	}
	w := napcat.NewWebUI(cfg.NapCat.WebUIURL, cfg.NapCat.WebUIToken)
	if err := w.Login(); err != nil {
		return nil, err
	}
	l.webui, l.webuiK = w, key
	return w, nil
}

// invalidateWebUI drops the cached bridge when a call proves its credential was
// refused.
//
// The credential is signed with NapCat's per-process secret unless
// NAPCAT_WEBUI_JWT_SECRET_KEY is set, so it dies with a NapCat restart — a
// routine event. Without this, the cached bridge kept replaying a dead
// credential and the fallback path stayed broken until the operator changed
// the configuration or restarted this service. The invalidation is narrow (see
// napcat.IsAuthFailure): a transport error or a business failure must not spend
// NapCat's login rate limit on a re-login.
func (l *lazyQQ) invalidateWebUI(err error) {
	if !napcat.IsAuthFailure(err) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.webui, l.webuiK = nil, ""
}

func (l *lazyQQ) GetGroupList() ([]napcat.Group, error) {
	return napcat.Failover(
		func() ([]napcat.Group, error) {
			c, err := l.onebot()
			if err != nil {
				return nil, err
			}
			return c.GetGroupList()
		},
		func() ([]napcat.Group, error) {
			w, err := l.webuiClient()
			if err != nil {
				return nil, err
			}
			gs, err := w.GetGroupList()
			l.invalidateWebUI(err)
			return gs, err
		},
	)
}

func (l *lazyQQ) GetGroupMemberList(groupID int64) ([]napcat.GroupMember, error) {
	return napcat.Failover(
		func() ([]napcat.GroupMember, error) {
			c, err := l.onebot()
			if err != nil {
				return nil, err
			}
			return c.GetGroupMemberList(groupID)
		},
		func() ([]napcat.GroupMember, error) {
			w, err := l.webuiClient()
			if err != nil {
				return nil, err
			}
			ms, err := w.GetGroupMemberList(groupID)
			l.invalidateWebUI(err)
			return ms, err
		},
	)
}

func (l *lazyQQ) GetGroupSignedList(groupID int64) ([]napcat.SignedMember, error) {
	return napcat.Failover(
		func() ([]napcat.SignedMember, error) {
			c, err := l.onebot()
			if err != nil {
				return nil, err
			}
			return c.GetGroupSignedList(groupID)
		},
		func() ([]napcat.SignedMember, error) {
			w, err := l.webuiClient()
			if err != nil {
				return nil, err
			}
			ss, err := w.GetGroupSignedList(groupID)
			l.invalidateWebUI(err)
			return ss, err
		},
	)
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
//
// While the live gateway is in a login cooldown the probe refuses instead of
// spending another attempt: the cooldown exists because the gateway answered a
// rate limit, and a connection test is exactly the kind of repeated click that
// keeps the window from closing. The operator gets the remaining wait.
func (p prober) ProbeGateway(url, username, password string) (*newapi.User, bool, error) {
	if p.gw != nil && p.gw.lockout != nil {
		if err := p.gw.lockout.Blocked(); err != nil {
			return nil, false, err
		}
	}
	if p.gw != nil && p.gw.matches(url, username, password) {
		c, err := p.gw.client()
		if err == nil {
			u, err := c.Self()
			if err == nil {
				return u, true, nil
			}
		}
		// The live attempt may have just been rate-limited, which arms the
		// cooldown. Retrying raw would spend the attempt the cooldown exists to
		// save, so report the wait instead.
		if p.gw.lockout != nil {
			if blocked := p.gw.lockout.Blocked(); blocked != nil {
				return nil, false, blocked
			}
		}
		// Fall through to a real login: the live session could not answer, and
		// the operator asked for a test, so they get one.
	}
	c := newapi.New(url)
	// Identify the throwaway client too. Without this its session row is
	// anonymous, which defeats the point of the fingerprint: the operator could
	// not tell this probe's leftover session from a real client's.
	if p.gw != nil {
		if secret := p.gw.cfg.SessionFingerprint(); secret != "" {
			c.SetFingerprint(newapi.NewFingerprint(secret))
		}
	}
	if err := c.Login(username, password); err != nil {
		// A rejected test arms the same cooldown a sweep would: without it the
		// next click tries again immediately and the window never closes.
		if p.gw != nil && p.gw.lockout != nil {
			p.gw.lockout.Fail(err)
		}
		// Known limit: if the gateway minted a session but the response did not
		// arrive intact (a transport drop, an unreadable body), that session is
		// not revoked — we hold no usable cookie to revoke it with. It ages out
		// in 30 days and the operator can revoke it from the panel meanwhile;
		// the alternative (revoking blind) needs a credential we do not have.
		return nil, false, err
	}
	u, err := c.Self()
	// Revoke the session the login just minted. The probe exists to prove the
	// credentials work, and it can only do that by logging in — but leaving the
	// session behind spends one of the account's 50 active sessions per click
	// (they live 30 days), so repeated testing would lock the account out.
	// A failed revoke is not worth failing the probe over: the credential check
	// already succeeded, so it is reported as a warning by the caller.
	if logoutErr := c.Logout(); logoutErr != nil {
		slog.Warn("new-api 连接测试：已登录但未能撤销测试会话，该会话会在 30 天后过期", "err", logoutErr)
	}
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

// MessageIsToday prevents delayed/replayed events from earning today's reward.
func (b bindAdapter) MessageIsToday(timestamp int64) bool {
	cfg, err := b.svc.Settings()
	if err != nil || timestamp <= 0 {
		return false
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return false
	}
	return time.Unix(timestamp, 0).In(loc).Format("2006-01-02") == time.Now().In(loc).Format("2006-01-02")
}

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

// notConfiguredError is returned when a stored connection setting is missing.
// Its message is shown to the operator (it reaches a toast through the API's
// error passthrough), so it is Simplified Chinese like the rest of the panel's
// UI text — the English form this replaced was the only such string left.
type notConfiguredError struct{}

func (*notConfiguredError) Error() string {
	return "尚未配置：请先在「连接配置」里填写 new-api 与 NapCat 的连接信息"
}
