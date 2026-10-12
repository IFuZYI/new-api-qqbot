package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every route the panel calls must exist on the router.
//
// The browser harness mocks fetch, so its cases prove the components and never
// the URLs: renaming /api/users in web/src/api.ts would keep all of them green
// while the shipped panel 404s. This reads the panel's own source, reduces each
// /api path to its literal segments, and resolves it against the registered
// routes.
func TestEveryPanelRouteExists(t *testing.T) {
	src, err := os.ReadFile("../../web/src/api.ts")
	if err != nil {
		t.Fatalf("read api.ts: %v", err)
	}
	routes := registeredRoutePatterns()
	if len(routes) < 20 {
		t.Fatalf("only %d routes were read from api.go; the extraction is broken", len(routes))
	}

	// Any /api path, stopping at a quote, backtick, ${, or ?.
	re := regexp.MustCompile(`/api/[A-Za-z0-9/_${}.-]*`)
	seen := map[string]bool{}
	var checked int
	for _, raw := range re.FindAllString(string(src), -1) {
		path := cleanPanelPath(raw)
		if path == "" || path == "/api" || seen[path] {
			continue
		}
		seen[path] = true
		checked++
		if !resolves(routes, path) {
			t.Errorf("the panel calls %s, which no route serves", path)
		}
	}
	if checked < 20 {
		t.Fatalf("only %d panel paths were checked; the extraction is broken", checked)
	}
}

// cleanPanelPath reduces a panel path to comparable segments.
//
// The panel builds paths as templates and appends query strings:
// `/api/users/${id}/group`, `/api/logs${qs ? '?' + qs : ”}`. A `${...}` tail is
// a placeholder for ONE segment, so it becomes `{id}`; a query suffix is
// dropped.
func cleanPanelPath(raw string) string {
	// Drop everything from the first ${ that ends the path portion.
	if i := strings.Index(raw, "${"); i >= 0 {
		head := raw[:i]
		// A template like /api/users/${id}/group: keep the literal tail after
		// the closing brace when it continues the path.
		rest := raw[i:]
		if j := strings.Index(rest, "}"); j >= 0 {
			tail := rest[j+1:]
			// A query suffix (e.g. ${qs ? '?' + qs : ''}) leaves a '?' or
			// stray text; keep the tail only when it is a clean path segment.
			if strings.HasPrefix(tail, "/") {
				head += "{id}" + strings.TrimRight(tail, "`'\" ")
			} else {
				head += "{id}"
			}
		}
		raw = head
	}
	if i := strings.IndexByte(raw, '?'); i > 0 {
		raw = raw[:i]
	}
	raw = strings.TrimRight(raw, "/`'\" ")
	return raw
}

// registeredRoutePatterns reads the patterns out of the router's own routes()
// function, so the test cannot drift from the registration it checks.
func registeredRoutePatterns() []string {
	src, err := os.ReadFile("api.go")
	if err != nil {
		panic(err)
	}
	re := regexp.MustCompile(`HandleFunc\("([A-Z]+ +/api/[^"]+)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		out = append(out, m[1])
	}
	return out
}

// resolves reports whether a panel path matches one of the router's patterns.
//
// The router is a hand-written matcher, so this mirrors its shape: a segment in
// braces matches any single segment.
func resolves(patterns []string, path string) bool {
	ps := splitPath(path)
	if len(ps) == 0 {
		return false
	}
	for _, pattern := range patterns {
		p := pattern
		if i := strings.IndexByte(p, ' '); i >= 0 {
			p = p[i+1:]
		}
		cs := splitPath(p)
		if len(cs) != len(ps) {
			continue
		}
		ok := true
		for i := range cs {
			if strings.HasPrefix(cs[i], "{") {
				continue
			}
			if cs[i] != ps[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}
