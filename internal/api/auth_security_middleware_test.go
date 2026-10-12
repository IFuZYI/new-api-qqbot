package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCSRFProtection(t *testing.T) {
	r := &Router{}
	// Use an interface so the initial RED reports absent behavior, not a compile error.
	protector, ok := any(r).(interface {
		csrfProtection(http.Handler) http.Handler
	})
	if !ok {
		t.Fatal("root-wide CSRF middleware is missing")
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	h := protector.csrfProtection(next)
	cases := []struct {
		name, method, origin, site string
		want                       int
	}{
		{"cli", "POST", "", "", 204},
		{"same origin", "POST", "https://panel.test", "", 204},
		{"metadata same origin", "POST", "", "same-origin", 204},
		{"cross origin", "POST", "https://evil.test", "", 403},
		{"cross site", "POST", "", "cross-site", 403},
		{"same site sibling", "POST", "", "same-site", 403},
		{"null", "POST", "null", "", 403},
		{"contradictory headers", "POST", "https://evil.test", "same-origin", 403},
		{"wrong scheme", "POST", "http://panel.test", "", 403},
		{"wrong port", "POST", "https://panel.test:444", "", 403},
		{"malformed", "POST", ":broken", "", 403},
		{"path in origin", "POST", "https://panel.test/path", "", 403},
		{"safe read", "GET", "https://evil.test", "cross-site", 204},
		{"safe head", "HEAD", "https://evil.test", "cross-site", 204},
		{"preflight", "OPTIONS", "https://evil.test", "cross-site", 204},
		{"delete", "DELETE", "https://evil.test", "", 403},
		{"put", "PUT", "https://evil.test", "", 403},
		{"patch", "PATCH", "https://evil.test", "", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "https://panel.test/future-unknown-route", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				req.Header.Set("Sec-Fetch-Site", tc.site)
			}
			req.Header.Set("X-Forwarded-Host", "evil.test")
			req.Header.Set("X-Forwarded-Proto", "http")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
}
