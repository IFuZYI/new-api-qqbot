package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"qqbot/internal/service"
)

// listUsers serves the operator's user table: a union of QQ members and new-api
// accounts, filterable on either side. See service.UserDirectory.
func (r *Router) listUsers(w http.ResponseWriter, req *http.Request) {
	date, err := r.svc.Today()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	q := req.URL.Query()
	f := service.UserFilter{
		Bound:   q.Get("bound"),
		InGroup: q.Get("in_group"),
		Group:   q.Get("group"),
		Keyword: q.Get("q"),
		Kind:    q.Get("kind"),
	}
	rows, err := r.svc.UserDirectory(f, date)
	if err != nil {
		writeErr(w, 500, err)
		return
	}

	// Totals are computed over the *filtered* set so the header matches what is
	// on screen, and over the unfiltered set so the operator can see how much
	// they are hiding.
	unfiltered, err := r.svc.UserDirectory(service.UserFilter{}, date)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"items":   rows,
		"total":   len(rows),
		"date":    date,
		"summary": userSummary(unfiltered),
	})
}

// userSummary counts the rows an operator scans first. The labels are chosen to
// mean what they say: `left` is a known QQ who is no longer in any monitored
// group (not merely a row without a group), and `not_joined` is an account with
// no QQ at all.
func userSummary(rows []service.UserRow) map[string]int {
	s := map[string]int{
		"total": len(rows),
		"bound": 0, "unbound": 0,
		"in_group": 0, "left": 0,
		"accounts": 0, "not_joined": 0,
	}
	for _, r := range rows {
		if r.Bound {
			s["bound"]++
		} else {
			s["unbound"]++
		}
		if r.InGroup {
			s["in_group"]++
		}
		if r.Kind == "left" {
			s["left"]++
		}
		if r.Kind == "account" {
			s["accounts"]++
			if r.QQ == 0 {
				s["not_joined"]++
			}
		}
	}
	return s
}

// syncUsers refreshes the cached new-api account list, which is what lets the
// table show accounts nobody has joined with.
func (r *Router) syncUsers(w http.ResponseWriter, _ *http.Request) {
	n, err := r.svc.SyncNewAPIUsers()
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"accounts": n})
}

// setUserGroup changes one account's group.
func (r *Router) setUserGroup(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.Atoi(req.PathValue("id"))
	if err != nil || id <= 0 {
		writeErr(w, 400, errors.New("invalid new-api user id"))
		return
	}
	var in struct {
		Group string `json:"group"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if err := r.svc.SetUserGroup(id, in.Group); err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "newapi_user_id": id, "group": strings.TrimSpace(in.Group)})
}

// setUserGroups changes the group of many accounts at once. Each account's
// outcome is reported separately: one protected account must not block the
// rest of the selection.
func (r *Router) setUserGroups(w http.ResponseWriter, req *http.Request) {
	var in struct {
		UserIDs []int  `json:"user_ids"`
		Group   string `json:"group"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	results, err := r.svc.SetUserGroups(in.UserIDs, in.Group)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	ok := 0
	for _, res := range results {
		if res.OK {
			ok++
		}
	}
	writeJSON(w, 200, map[string]any{
		"results":   results,
		"succeeded": ok,
		"failed":    len(results) - ok,
	})
}
