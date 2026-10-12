package napcat

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestR6RedirectProtectsSecrets(t *testing.T) {
	for _, mode := range []string{"onebot", "webui", "login"} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			for _, downgrade := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/downgrade=%v", mode, status, downgrade), func(t *testing.T) {
					var leaked atomic.Int32
					sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						leaked.Add(1)
						io.Copy(io.Discard, r.Body)
						fmt.Fprint(w, `{"code":0,"data":{"Credential":"stolen"},"status":"ok","retcode":0}`)
					}))
					defer sink.Close()
					handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL+"/steal", status) })
					var origin *httptest.Server
					if downgrade {
						origin = httptest.NewTLSServer(handler)
					} else {
						origin = httptest.NewServer(handler)
					}
					defer origin.Close()
					var err error
					if mode == "onebot" {
						c := New(origin.URL, "onebot-secret")
						c.http.Transport = origin.Client().Transport
						err = c.Call("action", nil, nil)
					} else {
						c := NewWebUI(origin.URL, "password-secret")
						c.http.Transport = origin.Client().Transport
						c.SetCredential("credential-secret")
						if mode == "login" {
							err = c.Login()
						} else {
							err = c.do("/api/action", map[string]string{"value": "secret"}, nil)
						}
					}
					if err == nil || leaked.Load() != 0 {
						t.Fatalf("redirect err=%v; destination requests=%d (must be zero)", err, leaked.Load())
					}
				})
			}
		}
	}
}

func TestR6SameOriginRedirectAndLimit(t *testing.T) {
	for _, mode := range []string{"onebot", "webui", "login"} {
		for _, loop := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/loop=%v", mode, loop), func(t *testing.T) {
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if loop || r.URL.Path != "/target" {
						http.Redirect(w, r, "/target", 307)
						return
					}
					if r.Header.Get("Authorization") != "Bearer secret" {
						t.Error("same-origin credential lost")
					}
					if mode == "login" {
						b, _ := io.ReadAll(r.Body)
						if len(b) == 0 {
							t.Error("login hash lost")
						}
					}
					fmt.Fprint(w, `{"code":0,"data":{"Credential":"new"},"status":"ok","retcode":0}`)
				}))
				defer srv.Close()
				var err error
				if mode == "onebot" {
					err = New(srv.URL, "secret").Call("action", nil, nil)
				} else {
					c := NewWebUI(srv.URL, "password")
					c.SetCredential("secret")
					if mode == "login" {
						err = c.Login()
					} else {
						err = c.do("/api/action", nil, nil)
					}
				}
				if loop {
					if err == nil || calls.Load() != 10 {
						t.Fatalf("loop err=%v calls=%d", err, calls.Load())
					}
				} else if err != nil || calls.Load() != 2 {
					t.Fatalf("safe redirect err=%v calls=%d", err, calls.Load())
				}
			})
		}
	}
}
