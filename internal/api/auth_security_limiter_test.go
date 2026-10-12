package api

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

type unreadableAuthBody struct{}

func (unreadableAuthBody) Read([]byte) (int, error) { panic("rate limited request read its body") }
func (unreadableAuthBody) Close() error             { return nil }

func TestAuthAdmissionBeforeBodyAndBcrypt(t *testing.T) {
	for _, path := range []string{"/api/auth/login", "/api/auth/setup"} {
		t.Run(path, func(t *testing.T) {
			r, _, _, _ := newBareRouter(t)
			for i := 0; i < 10; i++ {
				req := httptest.NewRequest("POST", "http://panel.test"+path, strings.NewReader("!"))
				req.RemoteAddr = fmt.Sprintf("192.0.2.1:%d", 1000+i)
				req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i))
				w := httptest.NewRecorder()
				r.Handler().ServeHTTP(w, req)
				if w.Code != 400 {
					t.Fatalf("attempt %d: %d %s", i+1, w.Code, w.Body.String())
				}
			}
			req := httptest.NewRequest("POST", "http://panel.test"+path, nil)
			req.RemoteAddr = "192.0.2.1:9999"
			req.Header.Set("X-Forwarded-For", "198.51.100.1")
			req.Body = unreadableAuthBody{}
			w := httptest.NewRecorder()
			r.Handler().ServeHTTP(w, req)
			if w.Code != 429 || w.Header().Get("Retry-After") == "" {
				t.Fatalf("limited attempt: %d %s retry=%q", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
			}
			req = httptest.NewRequest("POST", "http://panel.test"+path, strings.NewReader("!"))
			req.RemoteAddr = "192.0.2.2:1000"
			w = httptest.NewRecorder()
			r.Handler().ServeHTTP(w, req)
			if w.Code != 400 {
				t.Fatalf("independent peer: %d", w.Code)
			}
		})
	}
}
