// Package api exposes the WebUI's HTTP interface. It uses the standard library
// router (method-aware patterns), so the service needs no web framework.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"qqbot/internal/config"
	"qqbot/internal/logging"
	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/scheduler"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// Router wires the HTTP handlers to a store and the business service.
type Router struct {
	st  *store.Store
	svc *service.Service
	cfg *config.Manager
	mux *http.ServeMux

	// napcatAdmin writes NapCat's OneBot adapters on the connection page's
	// behalf ("give me the WebUI URL and I'll set up the rest"). Optional: when
	// unset the auto-configure endpoint reports that it is unavailable, and
	// every other endpoint is unaffected.
	napcatAdmin NapCatAdmin

	// prober runs the connection tests. Optional: when unset the test endpoints
	// report that testing is unavailable.
	prober Prober

	// patrols reports the periodic sweeps' health. Optional: when unset the
	// status endpoint reports no runs, which is what a process with the
	// scheduler not started looks like.
	patrols PatrolStatus

	// patrolRunner runs one patrol on demand (the 测试一次 button). Optional for
	// the same reason as patrols: without a scheduler there is nothing to run,
	// and the endpoint says so instead of pretending.
	patrolRunner PatrolRunner

	// logs is the daily log files, for the 日志 page and for applying the log
	// settings the moment they are saved. Optional (see SetLogs).
	logs *logging.Manager

	// setupMu serialises first-run setup (see auth.go).
	setupMu sync.Mutex
}

// NapCatAdmin can log in to a NapCat WebUI and write its OneBot adapters. It is
// an interface so the connection page's auto-configure path is testable without
// a live NapCat.
type NapCatAdmin interface {
	EnsureAdapters(webuiURL, webuiToken string, opts napcat.AdapterOptions) (*napcat.AdapterResult, error)
}

// Prober reaches the two external systems on the connection page's behalf, so
// the operator can verify what they typed before relying on it. It is separate
// from NapCatAdmin because a test only needs to read, never to write.
type Prober interface {
	// ProbeGateway reports the account the credentials belong to. The second
	// result says whether an existing session answered, so the UI can be honest
	// about what was actually verified.
	ProbeGateway(url, username, password string) (*newapi.User, bool, error)
	// ProbeWebUI logs in to NapCat's panel and reports the bot it drives.
	ProbeWebUI(webuiURL, webuiToken string) (*napcat.LoginInfo, error)
	// ProbeOneBot asks an OneBot endpoint for the bot identity.
	ProbeOneBot(onebotURL, onebotToken string) (*napcat.LoginInfo, error)
	// ProbeEventWS opens the event WebSocket and reports success.
	ProbeEventWS(wsURL, token string) error
}

// PatrolStatus reports when each periodic sweep last ran and whether it failed.
// It is an interface so the API can be tested without a running scheduler.
type PatrolStatus interface {
	Status() map[string]any
}

// PatrolRunner runs one patrol immediately, for the 测试一次 button. The result is
// the sweep's own (e.g. service.BindResult), so the UI can report what the run
// did rather than just "ok".
type PatrolRunner interface {
	RunNow(name string) (any, error)
}

// NewRouter builds the API router. The WebUI is served from webDist and
// connection settings are read and written through cfg.
func NewRouter(st *store.Store, svc *service.Service, cfg *config.Manager, webDist string) *Router {
	r := &Router{st: st, svc: svc, cfg: cfg, mux: http.NewServeMux()}
	r.routes()
	r.mux.Handle("/", staticHandler(webDist))
	return r
}

// SetNapCatAdmin installs the adapter-provisioning dependency used by the
// connection page's auto-configure action.
func (r *Router) SetNapCatAdmin(a NapCatAdmin) { r.napcatAdmin = a }

// SetProber installs the connection-test dependency.
func (r *Router) SetProber(p Prober) { r.prober = p }

// SetPatrolStatus installs the scheduler-status dependency.
func (r *Router) SetPatrolStatus(p PatrolStatus) { r.patrols = p }

// SetPatrolRunner installs the on-demand patrol runner.
func (r *Router) SetPatrolRunner(p PatrolRunner) { r.patrolRunner = p }

// Handler returns the router wrapped in the cross-origin boundary and panic
// recovery. csrfProtection sits outside the mux so a route added later inherits
// it without being listed; recoverPanics is outermost so a panic inside either
// layer still yields a response instead of a dropped connection.
//
// traceRequests sits inside recoverPanics, so a request that panicked is logged
// once (as the panic, with its stack) rather than also as a bare 500.
func (r *Router) Handler() http.Handler {
	return recoverPanics(traceRequests(r.csrfProtection(r.mux)))
}

func (r *Router) routes() {
	// Authentication. These must be reachable without a session: the UI needs
	// authState to choose between the login form and first-run setup.
	r.mux.HandleFunc("GET /api/auth/state", r.authState)
	r.mux.HandleFunc("POST /api/auth/setup", r.setup)
	r.mux.HandleFunc("POST /api/auth/login", r.login)
	r.mux.HandleFunc("POST /api/auth/logout", r.logout)

	// Serve the built WebUI. Assets live under /assets; everything else that is
	// not an API route falls back to index.html so the SPA can route client-side.
	r.mux.HandleFunc("GET /api/bootstrap", r.requireAuth(r.bootstrap))
	r.mux.HandleFunc("GET /api/config", r.requireAuth(r.getConfig))
	r.mux.HandleFunc("POST /api/config", r.requireAuth(r.saveConfig))
	// Section-scoped saves: the connection page keeps new-api and NapCat as two
	// independent cards, so a change to one never writes the other back from a
	// stale form.
	r.mux.HandleFunc("POST /api/config/newapi", r.requireAuth(r.saveNewAPIConfig))
	r.mux.HandleFunc("POST /api/config/napcat", r.requireAuth(r.saveNapCatConfig))
	// Auto-configure NapCat from just its WebUI address: log in, write the
	// OneBot adapters, and store the resulting OneBot/event URLs and token.
	r.mux.HandleFunc("POST /api/config/napcat/autoconfigure", r.requireAuth(r.autoConfigureNapCat))
	// Connection tests: each transport is probed on its own, so a failure names
	// the broken hop instead of just "cannot connect".
	r.mux.HandleFunc("POST /api/config/test/newapi", r.requireAuth(r.testNewAPI))
	r.mux.HandleFunc("POST /api/config/test/napcat", r.requireAuth(r.testNapCat))

	r.mux.HandleFunc("GET /api/groups", r.requireAuth(r.listGroups))
	r.mux.HandleFunc("POST /api/groups", r.requireAuth(r.saveGroups))
	r.mux.HandleFunc("POST /api/groups/refresh", r.requireAuth(r.refreshGroups))

	r.mux.HandleFunc("GET /api/members", r.requireAuth(r.listMembers))

	// User directory: a union of QQ members and new-api accounts, keyed on
	// both sides so accounts nobody has joined with are visible too.
	r.mux.HandleFunc("GET /api/users", r.requireAuth(r.listUsers))
	r.mux.HandleFunc("POST /api/users/sync", r.requireAuth(r.syncUsers))
	r.mux.HandleFunc("POST /api/users/{id}/group", r.requireAuth(r.setUserGroup))
	r.mux.HandleFunc("POST /api/users/groups", r.requireAuth(r.setUserGroups))

	r.mux.HandleFunc("GET /api/bindings", r.requireAuth(r.listBindings))
	r.mux.HandleFunc("POST /api/bindings", r.requireAuth(r.createBinding))
	r.mux.HandleFunc("POST /api/bindings/{qq}/delete", r.requireAuth(r.deleteBinding))

	r.mux.HandleFunc("GET /api/settings", r.requireAuth(r.getSettings))
	r.mux.HandleFunc("POST /api/settings", r.requireAuth(r.saveSettings))
	r.mux.HandleFunc("GET /api/status", r.requireAuth(r.status))
	// Run one patrol now, for the 测试一次 button on the patrol card. It is the
	// same sweep the timer runs, and it is recorded, so the row's health badge
	// updates instead of still claiming 尚未运行.
	r.mux.HandleFunc("POST /api/patrols/{name}/run", r.requireAuth(r.runPatrol))

	r.mux.HandleFunc("GET /api/checkins", r.requireAuth(r.listCheckins))

	// Logs: one day's records (filtered) and the list of days on disk.
	r.mux.HandleFunc("GET /api/logs", r.requireAuth(r.listLogs))
	r.mux.HandleFunc("GET /api/logs/days", r.requireAuth(r.listLogDays))

	// Group-conversion rules
	r.mux.HandleFunc("GET /api/rules", r.requireAuth(r.listRules))
	r.mux.HandleFunc("POST /api/rules", r.requireAuth(r.createRule))
	r.mux.HandleFunc("PUT /api/rules/{id}", r.requireAuth(r.updateRule))
	r.mux.HandleFunc("POST /api/rules/{id}/delete", r.requireAuth(r.deleteRule))
	r.mux.HandleFunc("GET /api/rules/meta", r.requireAuth(r.ruleMeta))
	r.mux.HandleFunc("GET /api/newapi/groups", r.requireAuth(r.newAPIGroups))

	r.mux.HandleFunc("POST /api/auth/password", r.requireAuth(r.changePassword))
	r.mux.HandleFunc("POST /api/actions/{name}", r.requireAuth(r.action))
}

// ---------- helpers ----------

// maxBodyBytes caps a decoded request body. The largest legitimate request is
// the NapCat connection form (URLs, tokens, adapter options) at a few KB, so
// 1 MiB leaves ample headroom while keeping the unauthenticated routes -
// /api/auth/state, setup, login and logout are reachable without a session -
// from letting anyone POST a body of arbitrary size and have it buffered.
const maxBodyBytes = 1 << 20

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already on the wire, so this cannot become an
		// error response; without the log the operator just sees a truncated
		// body and has nothing to correlate it with.
		slog.Error("编码响应失败", "err", err)
	}
}

// bodyTooLargeError is the readable form of the body-limit failure. decode
// returns it in place of the standard library's "http: request body too large",
// and writeErr renders it without the "invalid JSON: " prefix its callers would
// otherwise add, which misdescribes the problem.
type bodyTooLargeError struct{ limit int64 }

func (e *bodyTooLargeError) Error() string {
	return fmt.Sprintf("请求体过大（上限 %d 字节）", e.limit)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	noteErr(w, err)
	var tooLarge *bodyTooLargeError
	if errors.As(err, &tooLarge) {
		writeJSON(w, code, map[string]any{"error": tooLarge.Error()})
		return
	}
	writeJSON(w, code, map[string]any{"error": err.Error()})
}

// writeAuthErr is writeErr for the routes reachable without a session. Those
// responses must not carry internal detail - a SQLite message names the file
// and the statement - so the caller gets a generic line and the server keeps
// the detail in its log. Client mistakes (a bad password, a short one) are
// still reported as they were: they say nothing about the inside of the
// process, and hiding them would make the panel unusable.
func writeAuthErr(w http.ResponseWriter, code int, err error) {
	if code >= 500 {
		// The real error is attached to the request's log record (the ERROR
		// traceRequests writes for this 5xx); the client gets a generic line.
		noteErr(w, err)
		writeJSON(w, code, map[string]any{"error": "服务器内部错误，请查看服务端日志"})
		return
	}
	writeErr(w, code, err)
}

// recoverPanics turns a panic in a handler into a 500 for that one request.
//
// net/http already recovers, but it then closes the connection: the caller sees
// an EOF rather than a response, which in the panel is indistinguishable from
// "the button did nothing", and the process logs nothing at all. Recovering
// here keeps the connection usable and puts the method, path, panic value and
// stack in the log.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("请求处理时发生 panic", "method", req.Method, "path", req.URL.Path, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				writeJSON(w, 500, map[string]any{"error": "服务器内部错误，请查看服务端日志"})
			}
		}()
		next.ServeHTTP(w, req)
	})
}

func decode(r *http.Request, out any) error {
	// The limit is applied here, on every decoded body, because the size of
	// what arrives is not a property of any single handler.
	body := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer body.Close()
	dec := json.NewDecoder(body)
	if err := dec.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &bodyTooLargeError{limit: tooLarge.Limit}
		}
		return err
	}
	// Exactly one document: trailing content means the caller and this service
	// disagree about what the body is (a proxy appending a second object, a
	// truncated-then-continued upload), and silently using the first document
	// would act on a request that was never sent that way.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("请求体必须是单个 JSON 文档")
	}
	return nil
}

// ---------- handlers ----------

func (r *Router) bootstrap(w http.ResponseWriter, _ *http.Request) {
	cfg := r.cfg.MustLoad()
	settings, err := r.svc.Settings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	groups, err := r.st.ListGroups()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"configured": cfg.NewAPI.URL != "" && (cfg.NapCat.WebUIURL != "" || cfg.NapCat.OneBotURL != ""),
		"settings":   settings,
		"groups":     groups,
	})
}

func (r *Router) getConfig(w http.ResponseWriter, _ *http.Request) {
	// The revision lets the form detect that the configuration changed (a hand
	// edit to the file, or another tab) before it overwrites it. There is one
	// per section, so editing the gateway does not make an open NapCat card
	// look stale (and vice versa).
	writeJSON(w, 200, r.configResponse(r.cfg.MustLoad()))
}

func (r *Router) saveConfig(w http.ResponseWriter, req *http.Request) {
	var in struct {
		Config   config.Config `json:"config"`
		Revision string        `json:"revision"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}

	cfgIn, err := r.cfg.SaveCAS(in.Config, in.Revision)
	if err != nil {
		writeConfigErr(w, err)
		return
	}
	writeJSON(w, 200, r.configResponse(cfgIn))
}

// writeConfigErr distinguishes stale forms, invalid input and persistence errors.
func writeConfigErr(w http.ResponseWriter, err error) {
	var validation *config.ValidationError
	switch {
	case errors.Is(err, config.ErrConflict):
		writeErr(w, http.StatusConflict, err)
	case errors.As(err, &validation):
		writeErr(w, http.StatusBadRequest, err)
	default:
		writeErr(w, http.StatusInternalServerError, err)
	}
}

// configResponse is the shape the connection page reads back after a save.
func (r *Router) configResponse(c config.Config) map[string]any {
	return map[string]any{
		"config":   c.Redacted(),
		"revision": c.Revision(),
		"revisions": map[string]string{
			config.SectionNewAPI: c.SectionRevision(config.SectionNewAPI),
			config.SectionNapCat: c.SectionRevision(config.SectionNapCat),
		},
	}
}

// saveNewAPIConfig writes only the gateway half. The NapCat card is untouched,
// so a half-filled NapCat form cannot fail (or overwrite) a gateway save.
func (r *Router) saveNewAPIConfig(w http.ResponseWriter, req *http.Request) {
	r.saveSection(w, req, config.SectionNewAPI, func(in config.Config) config.Config {
		return config.Config{NewAPI: in.NewAPI}
	})
}

// saveNapCatConfig writes only the NapCat half.
func (r *Router) saveNapCatConfig(w http.ResponseWriter, req *http.Request) {
	r.saveSection(w, req, config.SectionNapCat, func(in config.Config) config.Config {
		return config.Config{NapCat: in.NapCat}
	})
}

// saveSection is the shared body of the two section saves: it guards on the
// section's own revision, applies the change and echoes the new revisions.
func (r *Router) saveSection(
	w http.ResponseWriter,
	req *http.Request,
	section string,
	pick func(config.Config) config.Config,
) {
	var in struct {
		Config   config.Config `json:"config"`
		Revision string        `json:"revision"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	c, err := r.cfg.SaveSectionCAS(section, pick(in.Config), in.Revision)
	if err != nil {
		writeConfigErr(w, err)
		return
	}
	writeJSON(w, 200, r.configResponse(c))
}

// autoConfigureNapCat takes the WebUI address and token the operator typed,
// logs in, writes the OneBot HTTP + WebSocket adapters NapCat needs, and stores
// the resulting OneBot URL, event URL and shared token back into the NapCat
// section. This is the "only the WebUI URL is required" path.
func (r *Router) autoConfigureNapCat(w http.ResponseWriter, req *http.Request) {
	if r.napcatAdmin == nil {
		writeErr(w, http.StatusNotImplemented, errors.New("本实例未启用 NapCat 自动配置"))
		return
	}
	var in struct {
		WebUIURL   string `json:"webui_url"`
		WebUIToken string `json:"webui_token"`
		Revision   string `json:"revision"`
		// Ports are optional; 0 means "use the defaults".
		HTTPPort int `json:"http_port"`
		WSPort   int `json:"ws_port"`
		// BindHost is the interface the OneBot adapters listen on. Empty means
		// 127.0.0.1, which is right when this service runs on the same host as
		// NapCat. A containerised service must use 0.0.0.0: it can never reach
		// the host's loopback, so an adapter bound to 127.0.0.1 is unreachable
		// from inside the container no matter what URL is configured.
		BindHost string `json:"bind_host"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}

	var res *napcat.AdapterResult
	var upstreamErr error
	c, err := r.cfg.UpdateSectionCAS(config.SectionNapCat, in.Revision, func(cur config.Config) (config.Config, error) {
		token := in.WebUIToken
		if token == "" || token == config.RedactedPassword {
			token = cur.NapCat.WebUIToken
		}
		webuiURL := strings.TrimSpace(in.WebUIURL)
		if webuiURL == "" {
			webuiURL = cur.NapCat.WebUIURL
		}
		if webuiURL == "" || token == "" {
			return config.Config{}, &config.ValidationError{Err: errors.New("请先填写 NapCat WebUI 地址与 Token")}
		}
		if err := (config.NapCat{WebUIURL: webuiURL, WebUIToken: token}).Validate(); err != nil {
			return config.Config{}, &config.ValidationError{Err: err}
		}
		res, upstreamErr = r.napcatAdmin.EnsureAdapters(webuiURL, token, napcat.AdapterOptions{
			Token:       cur.NapCat.OneBotToken,
			ConnectHost: napcat.WebUIHost(webuiURL),
			BindHost:    strings.TrimSpace(in.BindHost),
			HTTPPort:    in.HTTPPort, WSPort: in.WSPort,
		})
		if upstreamErr != nil {
			return config.Config{}, upstreamErr
		}
		cur.NapCat.WebUIURL, cur.NapCat.WebUIToken = webuiURL, token
		cur.NapCat.OneBotURL, cur.NapCat.OneBotToken = res.OneBotURL, res.Token
		cur.NapCat.EventWSURL, cur.NapCat.EventWSToken = res.EventWSURL, res.Token
		return cur, nil
	})
	if err != nil {
		if upstreamErr != nil {
			writeErr(w, 502, fmt.Errorf("配置 NapCat 失败：%w", err))
		} else {
			writeConfigErr(w, err)
		}
		return
	}
	out := r.configResponse(c)
	out["napcat"] = res
	writeJSON(w, 200, out)
}

func (r *Router) listGroups(w http.ResponseWriter, _ *http.Request) {
	gs, err := r.st.ListGroups()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": gs})
}

func (r *Router) refreshGroups(w http.ResponseWriter, _ *http.Request) {
	gs, err := r.svc.SyncGroups()
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, gs)
}

func (r *Router) saveGroups(w http.ResponseWriter, req *http.Request) {
	var in struct {
		GroupIDs []int64 `json:"group_ids"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	want := make(map[int64]bool, len(in.GroupIDs))
	for _, id := range in.GroupIDs {
		want[id] = true
	}

	gs, err := r.st.ListGroups()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	for _, g := range gs {
		if err := r.st.SetGroupEnabled(g.GroupID, want[g.GroupID]); err != nil {
			writeErr(w, 500, err)
			return
		}
	}
	out, err := r.st.ListGroups()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, out)
}

// memberView is one row of the user-management table.
type memberView struct {
	QQ           int64   `json:"qq"`
	Nickname     string  `json:"nickname"`
	GroupIDs     []int64 `json:"group_ids"`
	Bound        bool    `json:"bound"`
	NewAPIUserID int     `json:"newapi_user_id,omitempty"`
	NewAPIUser   string  `json:"newapi_username,omitempty"`
	NewAPIEmail  string  `json:"newapi_email,omitempty"`
	BindSource   string  `json:"bind_source,omitempty"`
	CheckedToday bool    `json:"checked_today"`
	CheckinDays  int     `json:"checkin_days"`
	RewardToday  int     `json:"reward_today"`
	// RewardState is pending, claimed, ambiguous or paid. Only paid means the
	// quota is confirmed granted; ambiguous needs operator reconciliation
	// because the gateway may or may not have credited it.
	RewardState string `json:"reward_state,omitempty"`
	RewardError string `json:"reward_error,omitempty"`
}

func (r *Router) listMembers(w http.ResponseWriter, _ *http.Request) {
	date, err := r.svc.Today()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	ms, err := r.st.ListMembers()
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	out := make([]memberView, 0, len(ms))
	for _, m := range ms {
		v := memberView{QQ: m.QQ, Nickname: m.Nickname, GroupIDs: m.GroupIDs}
		if b, err := r.st.GetBinding(m.QQ); err == nil {
			v.Bound = true
			v.NewAPIUserID = b.NewAPIUserID
			v.NewAPIUser = b.NewAPIUsername
			v.NewAPIEmail = b.NewAPIEmail
			v.BindSource = b.Source
		}
		if c, err := r.st.GetCheckin(m.QQ, date); err == nil {
			v.CheckedToday = true
			v.RewardToday = c.RewardQuota
			v.RewardState = c.RewardState
			v.RewardError = c.RewardError
		}
		if n, err := r.st.CountCheckins(m.QQ); err == nil {
			v.CheckinDays = n
		}
		out = append(out, v)
	}
	writeJSON(w, 200, map[string]any{"items": out, "total": len(out), "date": date})
}

func (r *Router) listBindings(w http.ResponseWriter, _ *http.Request) {
	bs, err := r.st.ListBindings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": bs})
}

// createBinding is the manual 换绑 path: the operator names a QQ and a new-api
// account.
//
// The account's details are filled in from the cached account list when the
// caller does not supply them, so binding from the account side (where the
// operator picked an account, not typed its email) still records a complete
// row - and a binding whose stored email is blank can never be matched against
// a member's qq@qq.com again.
func (r *Router) createBinding(w http.ResponseWriter, req *http.Request) {
	var in struct {
		QQ             int64  `json:"qq"`
		NewAPIUserID   int    `json:"newapi_user_id"`
		NewAPIUsername string `json:"newapi_username"`
		NewAPIEmail    string `json:"newapi_email"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if in.QQ <= 0 {
		writeErr(w, 400, errors.New("qq 必须是正整数"))
		return
	}
	if in.NewAPIUserID <= 0 {
		writeErr(w, 400, errors.New("newapi_user_id 必须是正整数"))
		return
	}
	// Fill the account's identity from the cache rather than trusting the form:
	// the operator picks an account from a list, so the authoritative username
	// and email are the ones the gateway reported, not a possibly-blank field.
	if a, err := r.st.GetNewAPIUser(in.NewAPIUserID); err == nil {
		if in.NewAPIUsername == "" {
			in.NewAPIUsername = a.Username
		}
		if in.NewAPIEmail == "" {
			in.NewAPIEmail = a.Email
		}
	}
	if err := r.st.SetBinding(store.Binding{
		QQ: in.QQ, NewAPIUserID: in.NewAPIUserID,
		NewAPIUsername: in.NewAPIUsername, NewAPIEmail: in.NewAPIEmail, Source: "manual",
	}); err != nil {
		writeErr(w, 500, err)
		return
	}
	b, err := r.st.GetBinding(in.QQ)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// Binding and group conversion go together, whether the binding came from
	// the automatic match, the in-group command or the operator: "on bind"
	// means on bind. A rule failure must not fail the binding itself, so the
	// result is reported alongside it.
	rules, ruleErr := r.svc.ApplyOnBindRules(in.QQ, nil)
	resp := map[string]any{"binding": b, "rules": rules}
	if ruleErr != nil {
		resp["rule_error"] = ruleErr.Error()
	}
	writeJSON(w, 200, resp)
}

func (r *Router) deleteBinding(w http.ResponseWriter, req *http.Request) {
	qq, err := strconv.ParseInt(req.PathValue("qq"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("invalid qq"))
		return
	}
	if err := r.st.DeleteBinding(qq); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (r *Router) getSettings(w http.ResponseWriter, _ *http.Request) {
	s, err := r.svc.Settings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, s)
}

// status reports each patrol's last run and error alongside the switches.
//
// Both halves are needed to read it: an empty last-run time means "not run yet"
// or "switched off", and only the switch tells the two apart. Without the
// settings the panel would show a stopped patrol as a broken one.
func (r *Router) status(w http.ResponseWriter, _ *http.Request) {
	s, err := r.svc.Settings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	patrols := map[string]any{}
	if r.patrols != nil {
		patrols = r.patrols.Status()
	}
	writeJSON(w, 200, map[string]any{
		"patrols":  patrols,
		"settings": s,
	})
}

// runPatrol runs one patrol immediately, for the 测试一次 button.
//
// The name is validated against the scheduler's own table rather than a list
// kept here: a second list would drift, and a name this endpoint accepts but the
// scheduler does not know would be reported as success.
func (r *Router) runPatrol(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	if r.patrolRunner == nil {
		writeErr(w, 503, errors.New("巡检尚未启动，无法手动执行"))
		return
	}
	res, err := r.patrolRunner.RunNow(name)
	if err != nil {
		// An unknown name is the caller's mistake (404); a sweep that failed is
		// an upstream problem (502). They read differently to the operator.
		if errors.Is(err, scheduler.ErrUnknownPatrol) {
			writeErr(w, 404, err)
			return
		}
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"patrol": name, "result": res})
}

func (r *Router) saveSettings(w http.ResponseWriter, req *http.Request) {
	var in service.Settings
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if err := r.svc.SaveSettings(in); err != nil {
		writeErr(w, 400, err)
		return
	}
	s, err := r.svc.Settings()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// Apply the log settings now rather than at the next maintenance tick: an
	// operator switching to DEBUG wants the very next request traced, and a
	// shortened retention should take effect when they press 保存.
	if r.logs != nil {
		r.logs.Apply(logging.Policy{Level: s.LogLevel, RetentionDays: s.LogRetentionDays})
	}
	writeJSON(w, 200, s)
}

func (r *Router) listCheckins(w http.ResponseWriter, req *http.Request) {
	date := req.URL.Query().Get("date")
	if date == "" {
		d, err := r.svc.Today()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		date = d
	} else if _, err := time.Parse(time.DateOnly, date); err != nil {
		// The date reaches a query, so a typo must be refused rather than
		// answered with an empty list: "no check-ins that day" is
		// indistinguishable from a misspelled day, and the logs endpoint
		// already rejects a malformed limit the same way.
		writeErr(w, 400, errors.New("date 必须是 YYYY-MM-DD 格式"))
		return
	}
	cs, err := r.st.CheckinsOn(date)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": cs, "date": date})
}

// action runs one sweep on demand. The same code paths run on the scheduler,
// so the operator can force a run without waiting.
func (r *Router) action(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	date, err := r.svc.Today()
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	switch name {
	case "sync":
		if err := r.svc.SyncAllMembers(); err != nil {
			writeErr(w, 502, err)
			return
		}
		ms, err := r.st.ListMembers()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"members": len(ms)})

	case "bind":
		res, err := r.svc.AutoBind()
		if err != nil {
			writeErr(w, 502, err)
			return
		}
		writeJSON(w, 200, res)

	case "promote":
		// Kept as an alias so an old bookmark still works; it now runs the
		// configured rules rather than a blanket promotion.
		res, err := r.svc.RunScheduledRules()
		if err != nil {
			writeErr(w, 502, err)
			return
		}
		writeJSON(w, 200, map[string]any{"rules": res})

	case "rules":
		res, err := r.svc.RunScheduledRules()
		if err != nil {
			writeErr(w, 502, err)
			return
		}
		writeJSON(w, 200, map[string]any{"rules": res})

	case "checkin":
		if err := r.svc.SyncAllCheckins(date); err != nil {
			writeErr(w, 502, err)
			return
		}
		cs, err := r.st.CheckinsOn(date)
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"date": date, "checked": len(cs)})

	case "checkin-member":
		// Record a message-driven check-in for one member, so an operator can
		// verify that path (and pay its reward) without waiting for someone to
		// chat. It is the same code the event listener runs, including the
		// once-per-day rule.
		var in struct {
			QQ      int64 `json:"qq"`
			GroupID int64 `json:"group_id"`
		}
		if err := decode(req, &in); err != nil {
			writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
			return
		}
		if in.QQ == 0 || in.GroupID == 0 {
			writeErr(w, 400, errors.New("需要同时提供 qq 与 group_id"))
			return
		}
		res, err := r.svc.CheckinByMessage(in.GroupID, in.QQ)
		if err != nil {
			writeErr(w, 502, err)
			return
		}
		writeJSON(w, 200, map[string]any{
			"date": date, "qq": in.QQ,
			"recorded": res.Recorded, "already_checked_in": res.AlreadyCheckedIn,
			"unbound": res.Unbound,
		})

	case "award":
		res, err := r.svc.Award(date)
		if err != nil {
			writeErr(w, 502, err)
			return
		}
		writeJSON(w, 200, res)

	default:
		writeErr(w, 404, fmt.Errorf("unknown action %q", name))
	}
}
