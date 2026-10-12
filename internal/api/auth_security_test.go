package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qqbot/internal/config"
)

// This exercises the exported root, including routes added after the middleware.
func TestRootRejectsCrossOriginUnsafeRequests(t *testing.T) {
	r, _, _, _ := newBareRouter(t)
	r.mux.HandleFunc("POST /future-route", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	paths := []string{"/api/auth/setup", "/api/auth/login", "/api/auth/logout", "/api/auth/password", "/api/config", "/api/config/newapi", "/api/config/napcat", "/api/config/napcat/autoconfigure", "/api/config/test/newapi", "/api/config/test/napcat", "/api/groups", "/api/groups/refresh", "/api/users/sync", "/api/users/1/group", "/api/users/groups", "/api/bindings", "/api/bindings/1/delete", "/api/settings", "/api/patrols/bind/run", "/api/rules", "/api/rules/1/delete", "/api/actions/award", "/future-route", "/unknown-spa"}
	for _, path := range paths {
		for _, headers := range []map[string]string{
			{"Origin": "https://evil.test"},
			{"Sec-Fetch-Site": "cross-site", "Content-Type": "text/plain"},
			{"Sec-Fetch-Site": "same-site"},
			{"Origin": "null"},
			{"Origin": "https://evil.test", "Sec-Fetch-Site": "same-origin"},
			{"Origin": "https://evil.test", "X-Forwarded-Host": "evil.test", "X-Forwarded-Proto": "https"},
		} {
			req := httptest.NewRequest("POST", "http://panel.test"+path, strings.NewReader(`{}`))
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			r.Handler().ServeHTTP(w, req)
			if w.Code != 403 {
				t.Errorf("%s %v: got %d, want 403", path, headers, w.Code)
			}
		}
	}
	for _, method := range []string{"PUT", "PATCH", "DELETE"} {
		req := httptest.NewRequest(method, "http://panel.test/api/rules/1", strings.NewReader(`{}`))
		req.Header.Set("Origin", "https://evil.test")
		w := httptest.NewRecorder()
		r.Handler().ServeHTTP(w, req)
		if w.Code != 403 {
			t.Errorf("%s: %d", method, w.Code)
		}
	}
}

func TestPasswordByteLimit(t *testing.T) {
	for _, pw := range []string{strings.Repeat("a", 73), strings.Repeat("密", 25)} {
		t.Run(fmt.Sprint(len(pw)), func(t *testing.T) {
			r, _, srv, cl := newBareRouter(t)
			code, body := post(t, cl, srv.URL+"/api/auth/setup", fmt.Sprintf(`{"username":"admin","password":%q}`, pw))
			if code != 400 || !strings.Contains(body, "72 字节") {
				t.Errorf("setup: %d %s", code, body)
			}
			err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "env", Password: pw})
			if err == nil || !strings.Contains(err.Error(), "72 字节") {
				t.Errorf("env: %v", err)
			}
			code, body = post(t, cl, srv.URL+"/api/auth/setup", `{"username":"admin","password":"password123"}`)
			if code != 200 {
				t.Fatalf("setup: %d %s", code, body)
			}
			code, body = post(t, cl, srv.URL+"/api/auth/password", fmt.Sprintf(`{"current":"password123","next":%q}`, pw))
			if code != 400 || !strings.Contains(body, "72 字节") {
				t.Errorf("change: %d %s", code, body)
			}
		})
	}
}

func TestPasswordAccepts72Bytes(t *testing.T) {
	_, _, srv, cl := newBareRouter(t)
	code, body := post(t, cl, srv.URL+"/api/auth/setup", fmt.Sprintf(`{"username":"admin","password":%q}`, strings.Repeat("密", 24)))
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSessionCookieTransport(t *testing.T) {
	for _, scheme := range []string{"https", "http"} {
		t.Run(scheme, func(t *testing.T) {
			r, _, _, _ := newBareRouter(t)
			req := httptest.NewRequest("POST", scheme+"://panel.test/api/auth/setup", strings.NewReader(`{"username":"admin","password":"password123"}`))
			req.Header.Set("X-Forwarded-Proto", "https")
			w := httptest.NewRecorder()
			r.Handler().ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatal(cookies)
			}
			c := cookies[0]
			if c.Secure != (scheme == "https") || !c.HttpOnly {
				t.Errorf("set cookie: %+v", c)
			}
			req = httptest.NewRequest("POST", scheme+"://panel.test/api/auth/logout", nil)
			req.AddCookie(c)
			w = httptest.NewRecorder()
			r.Handler().ServeHTTP(w, req)
			cleared := w.Result().Cookies()[0]
			if cleared.Secure != c.Secure || cleared.SameSite != c.SameSite || cleared.Path != c.Path || !cleared.HttpOnly || cleared.MaxAge != -1 {
				t.Errorf("clear cookie: %+v; set: %+v", cleared, c)
			}
		})
	}
}
