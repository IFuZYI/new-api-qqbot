package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"qqbot/internal/config"
)

// Assert real credential/generation queries occur under the common writer lock.
// Mutating any individual handler's lock must fail, regardless of scheduling.
func TestAuthCredentialCriticalSections(t *testing.T) {
	for _, operation := range []string{"login", "logout", "password", "provision"} {
		t.Run(operation, func(t *testing.T) {
			r, st, _, _ := newBareRouter(t)
			if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "password123"}); err != nil {
				t.Fatal(err)
			}
			key, err := r.loadSessionKey()
			if err != nil {
				t.Fatal(err)
			}
			cookie := &http.Cookie{Name: sessionCookie, Value: signSession(key, "admin", 0, time.Now().Add(time.Hour))}
			observed := 0
			const callback = "test:credential-lock"
			err = st.DB().Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table != "admins" && tx.Statement.Table != "settings" {
					return
				}
				observed++
				if r.setupMu.TryLock() {
					r.setupMu.Unlock()
					t.Errorf("%s credential/generation read outside serialization (%s)", operation, tx.Statement.Table)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			defer st.DB().Callback().Query().Remove(callback)
			if operation == "provision" {
				if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "newpassword123"}); err != nil {
					t.Fatal(err)
				}
			} else {
				body := `{"username":"admin","password":"password123"}`
				if operation == "password" {
					body = `{"current":"password123","next":"newpassword123"}`
				}
				req := httptest.NewRequest("POST", "http://panel.test/api/auth/"+operation, strings.NewReader(body))
				req.AddCookie(cookie)
				w := httptest.NewRecorder()
				// Call the auth mutation directly: requireAuth is deliberately not locked.
				switch operation {
				case "login":
					r.login(w, req)
				case "logout":
					r.logout(w, req)
				case "password":
					r.changePassword(w, req)
				}
				if w.Code != 200 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
			}
			if observed == 0 {
				t.Fatal("no credential/generation query observed")
			}
		})
	}
}

func TestLoginCannotAcquirePostChangeGeneration(t *testing.T) {
	r, st, _, _ := newBareRouter(t)
	if err := r.EnsureAdminFromEnv(config.AdminCreds{Set: true, Username: "admin", Password: "password123"}); err != nil {
		t.Fatal(err)
	}
	key, err := r.loadSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: signSession(key, "admin", 0, time.Now().Add(time.Hour))}
	verified := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	// Login reads the session key only after successful credential verification.
	err = st.DB().Callback().Query().Before("gorm:query").Register("test:pause-login", func(tx *gorm.DB) {
		if tx.Statement.Table == "settings" {
			once.Do(func() { close(verified); <-release })
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB().Callback().Query().Remove("test:pause-login")
	loginDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r.login(w, httptest.NewRequest("POST", "http://panel.test/api/auth/login", strings.NewReader(`{"username":"admin","password":"password123"}`)))
		loginDone <- w
	}()
	<-verified
	changeDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("POST", "http://panel.test/api/auth/password", strings.NewReader(`{"current":"password123","next":"newpassword123"}`))
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		r.changePassword(w, req)
		changeDone <- w
	}()
	// The common lock must still be held after verification and before signing.
	if r.setupMu.TryLock() {
		r.setupMu.Unlock()
		t.Error("login released serialization before session issuance")
	}
	close(release)
	login := <-loginDone
	change := <-changeDone
	if login.Code != 200 || change.Code != 200 {
		t.Fatalf("login %d / change %d", login.Code, change.Code)
	}
	req := httptest.NewRequest("GET", "http://panel.test/api/auth/state", nil)
	req.AddCookie(login.Result().Cookies()[0])
	if r.currentUser(req) != "" {
		t.Fatal("old-password login acquired post-change generation")
	}
}

func TestCredentialLockDoesNotBlockReadAPI(t *testing.T) {
	r, _, _, _ := newBareRouter(t)
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	w := httptest.NewRecorder()
	r.authState(w, httptest.NewRequest("GET", "http://panel.test/api/auth/state", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
