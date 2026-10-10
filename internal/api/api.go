// Package api exposes the WebUI's HTTP interface. It uses the standard library
// router (method-aware patterns), so the service needs no web framework.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"qqbot/internal/config"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// Router wires the HTTP handlers to a store and the business service.
type Router struct {
	st  *store.Store
	svc *service.Service
	mux *http.ServeMux
}

// NewRouter builds the API router. The WebUI is served from webDist.
func NewRouter(st *store.Store, svc *service.Service, webDist string) *Router {
	r := &Router{st: st, svc: svc, mux: http.NewServeMux()}
	r.routes()
	r.mux.Handle("/", staticHandler(webDist))
	return r
}

// Handler returns the root handler.
func (r *Router) Handler() http.Handler { return r.mux }

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

	r.mux.HandleFunc("GET /api/groups", r.requireAuth(r.listGroups))
	r.mux.HandleFunc("POST /api/groups", r.requireAuth(r.saveGroups))
	r.mux.HandleFunc("POST /api/groups/refresh", r.requireAuth(r.refreshGroups))

	r.mux.HandleFunc("GET /api/members", r.requireAuth(r.listMembers))

	r.mux.HandleFunc("GET /api/bindings", r.requireAuth(r.listBindings))
	r.mux.HandleFunc("POST /api/bindings", r.requireAuth(r.createBinding))
	r.mux.HandleFunc("POST /api/bindings/{qq}/delete", r.requireAuth(r.deleteBinding))

	r.mux.HandleFunc("GET /api/settings", r.requireAuth(r.getSettings))
	r.mux.HandleFunc("POST /api/settings", r.requireAuth(r.saveSettings))

	r.mux.HandleFunc("GET /api/checkins", r.requireAuth(r.listCheckins))

	r.mux.HandleFunc("POST /api/auth/password", r.requireAuth(r.changePassword))
	r.mux.HandleFunc("POST /api/actions/{name}", r.requireAuth(r.action))
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"error": err.Error()})
}

func decode(r *http.Request, out any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	return dec.Decode(out)
}

// ---------- handlers ----------

func (r *Router) bootstrap(w http.ResponseWriter, _ *http.Request) {
	cfg, err := config.Load(r.st)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
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
	cfg, err := config.Load(r.st)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, cfg.Redacted())
}

func (r *Router) saveConfig(w http.ResponseWriter, req *http.Request) {
	var in config.Config
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	stored, err := config.Load(r.st)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	in = in.Merge(stored)

	if err := in.NewAPI.Validate(); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := in.NapCat.Validate(); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := config.Save(r.st, in); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, in.Redacted())
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

// createBinding is the manual 换绑 path: the operator names any new-api account.
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
	if in.QQ == 0 {
		writeErr(w, 400, errors.New("qq is required"))
		return
	}
	if in.NewAPIUserID == 0 {
		writeErr(w, 400, errors.New("newapi_user_id is required"))
		return
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
	writeJSON(w, 200, b)
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
		res, err := r.svc.Promote()
		if err != nil {
			writeErr(w, 502, err)
			return
		}
		writeJSON(w, 200, res)

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
