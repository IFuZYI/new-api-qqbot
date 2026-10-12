package api

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"time"
	"weak"
)

const (
	authAttemptBurst    = 10
	authAttemptWindow   = time.Minute
	authAttemptCapacity = 4096
)

type authAttemptKey struct {
	router weak.Pointer[Router]
	peer   string
}
type authAttemptWindowState struct {
	used  int
	until time.Time
}
type authAttemptLimiter struct {
	mu      sync.Mutex
	entries map[authAttemptKey]authAttemptWindowState
}

// One bounded process-local table; weak router identities avoid retaining old
// routers/stores. Setup and login share a peer budget. At capacity fail closed
// for new peers rather than evicting an attacker's active cooldown.
var authAttempts authAttemptLimiter

func (l *authAttemptLimiter) allow(key authAttemptKey, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[authAttemptKey]authAttemptWindowState)
	}
	state, exists := l.entries[key]
	if exists && !now.Before(state.until) {
		delete(l.entries, key)
		exists = false
	}
	if !exists {
		if len(l.entries) >= authAttemptCapacity {
			for k, v := range l.entries {
				if !now.Before(v.until) || k.router.Value() == nil {
					delete(l.entries, k)
				}
			}
			if len(l.entries) >= authAttemptCapacity {
				return authAttemptWindow
			}
		}
		state = authAttemptWindowState{until: now.Add(authAttemptWindow)}
	}
	if state.used >= authAttemptBurst {
		return state.until.Sub(now)
	}
	state.used++
	l.entries[key] = state
	return 0
}

func authPeer(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	return ip.Unmap().WithZone("").String()
}

func (r *Router) admitAuthAttempt(w http.ResponseWriter, req *http.Request) bool {
	retry := authAttempts.allow(authAttemptKey{weak.Make(r), authPeer(req)}, time.Now())
	if retry == 0 {
		return true
	}
	seconds := (retry + time.Second - 1) / time.Second
	w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
	writeErr(w, http.StatusTooManyRequests, errors.New("尝试过于频繁，请稍后重试"))
	return false
}

// csrfProtection belongs outside the root mux, not on a list of today's routes:
// future mutations and SPA fallbacks must inherit the same browser boundary.
func (r *Router) csrfProtection(next http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusForbidden, errors.New("不允许跨源请求"))
	}))
	protected := protection.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			// Check Origin even when Fetch Metadata says same-origin. The standard
			// guard prioritizes metadata and compares only Host in its fallback.
			if origins, present := req.Header["Origin"]; present {
				scheme := "http"
				if req.TLS != nil {
					scheme = "https"
				}
				origin, err := url.Parse(req.Header.Get("Origin"))
				if len(origins) != 1 || err != nil || origin.Scheme != scheme || origin.Host != req.Host || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
					writeErr(w, http.StatusForbidden, errors.New("不允许跨源请求"))
					return
				}
			}
		}
		protected.ServeHTTP(w, req)
	})
}
