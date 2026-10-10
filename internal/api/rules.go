package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"qqbot/internal/service"
)

func (r *Router) listRules(w http.ResponseWriter, _ *http.Request) {
	rules, err := r.svc.Rules()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": rules})
}

// ruleMeta tells the UI which condition kinds and operators exist, so the form
// stays in step with the server instead of hard-coding them twice.
func (r *Router) ruleMeta(w http.ResponseWriter, _ *http.Request) {
	types := make([]map[string]string, 0, len(service.ConditionTypes))
	for _, t := range service.ConditionTypes {
		types = append(types, map[string]string{"value": string(t), "label": t.Label()})
	}
	ops := make([]string, 0, len(service.ConditionOps))
	for _, o := range service.ConditionOps {
		ops = append(ops, string(o))
	}
	writeJSON(w, 200, map[string]any{
		"condition_types": types,
		"operators":       ops,
	})
}

func (r *Router) createRule(w http.ResponseWriter, req *http.Request) {
	var in service.Rule
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	in.ID = 0
	created, err := r.svc.CreateRule(in)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, created)
}

func (r *Router) updateRule(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("invalid id"))
		return
	}
	var in service.Rule
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	in.ID = id
	updated, err := r.svc.UpdateRule(in)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, updated)
}

func (r *Router) deleteRule(w http.ResponseWriter, req *http.Request) {
	id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, errors.New("invalid id"))
		return
	}
	if err := r.svc.DeleteRule(id); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// newAPIGroups returns the group names configured on the gateway, so the rule
// form can offer them instead of asking the operator to type them.
func (r *Router) newAPIGroups(w http.ResponseWriter, _ *http.Request) {
	groups, err := r.svc.NewAPIGroups()
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, groups)
}
