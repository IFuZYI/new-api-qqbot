package newapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// okEnvelope wraps a payload the way new-api does.
func okEnvelope(data any) string {
	b, _ := json.Marshal(map[string]any{"success": true, "message": "", "data": data})
	return string(b)
}

func TestLoginStoresTokenAndSendsCredentials(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/login" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		// Cloudflare fronts the real instance and rejects the default Go UA.
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "qqbot") {
			t.Errorf("User-Agent not set to an identifying value: %q", ua)
		}
		_, _ = w.Write([]byte(okEnvelope(map[string]any{"access_token": "tok-123", "token_type": "Bearer"})))
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login("napcat", "pw"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if gotBody["username"] != "napcat" || gotBody["password"] != "pw" {
		t.Errorf("credentials not sent: %+v", gotBody)
	}
	if c.Token() != "tok-123" {
		t.Errorf("token not stored: %q", c.Token())
	}
}

func TestLoginFailsOnSuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"Username or password is incorrect"}`))
	}))
	defer srv.Close()

	err := New(srv.URL).Login("napcat", "bad")
	if err == nil {
		t.Fatal("expected an error for a rejected login")
	}
	if !strings.Contains(err.Error(), "incorrect") {
		t.Errorf("error should carry the server message, got %v", err)
	}
}

// The search endpoint is a LIKE across username/email/display_name, so the
// client must narrow it to an exact, case-insensitive email match itself.
func TestSearchUserByEmailRequiresExactMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/search" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("keyword") != "54321@qq.com" {
			t.Errorf("keyword not passed: %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(okEnvelope(map[string]any{
			"items": []map[string]any{
				{"id": 7, "username": "seven", "email": "x54321@qq.com", "group": "default", "role": 1},
				{"id": 8, "username": "eight", "email": "54321@QQ.com", "group": "default", "role": 1},
			},
			"total": 2,
		})))
	}))
	defer srv.Close()

	c := New(srv.URL)
	u, err := c.FindUserByEmail("54321@qq.com")
	if err != nil {
		t.Fatalf("FindUserByEmail: %v", err)
	}
	if u == nil || u.ID != 8 {
		t.Fatalf("want the exact match id=8, got %+v", u)
	}
}

func TestFindUserByEmailReturnsNilWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okEnvelope(map[string]any{
			"items": []map[string]any{{"id": 1, "username": "a", "email": "nope@qq.com"}},
			"total": 1,
		})))
	}))
	defer srv.Close()

	u, err := New(srv.URL).FindUserByEmail("54321@qq.com")
	if err != nil {
		t.Fatalf("FindUserByEmail: %v", err)
	}
	if u != nil {
		t.Errorf("expected nil for no exact match, got %+v", u)
	}
}

// Two accounts with the same email should never be bound automatically.
func TestFindUserByEmailRejectsAmbiguousMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okEnvelope(map[string]any{
			"items": []map[string]any{
				{"id": 1, "username": "a", "email": "54321@qq.com"},
				{"id": 2, "username": "b", "email": "54321@qq.com"},
			},
			"total": 2,
		})))
	}))
	defer srv.Close()

	u, err := New(srv.URL).FindUserByEmail("54321@qq.com")
	if !errors.Is(err, ErrAmbiguousEmail) {
		t.Fatalf("want ErrAmbiguousEmail, got u=%+v err=%v", u, err)
	}
}

// PUT /api/user/ discards read-only fields, so the client must resend the whole
// user object or it will blank out username/display_name/role.
func TestSetGroupSendsFullUserObject(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/user/" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("token not attached: %q", auth)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(okEnvelope(nil)))
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.SetToken("tok")
	u := &User{ID: 8, Username: "eight", DisplayName: "Eight", Role: 1, Remark: "note"}
	if err := c.SetGroup(u, "authorized"); err != nil {
		t.Fatalf("SetGroup: %v", err)
	}

	for k, want := range map[string]any{
		"id": float64(8), "username": "eight", "display_name": "Eight",
		"role": float64(1), "group": "authorized", "remark": "note",
	} {
		if gotBody[k] != want {
			t.Errorf("body[%q] = %v, want %v", k, gotBody[k], want)
		}
	}
}

func TestSetGroupSurfacesServerRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"No permission to update users of same or higher permission level"}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.SetToken("tok")
	err := c.SetGroup(&User{ID: 8, Username: "eight", Role: 1}, "authorized")
	if err == nil {
		t.Fatal("expected the rejection to surface as an error")
	}
}

func TestAddQuotaSendsExpectedPayload(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/user/manage" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(okEnvelope(nil)))
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.SetToken("tok")
	if err := c.AddQuota(8, 150); err != nil {
		t.Fatalf("AddQuota: %v", err)
	}
	want := map[string]any{"id": float64(8), "action": "add_quota", "mode": "add", "value": float64(150)}
	for k, v := range want {
		if gotBody[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, gotBody[k], v)
		}
	}
}

func TestAddQuotaRejectsNonPositive(t *testing.T) {
	c := New("http://127.0.0.1:1")
	c.SetToken("tok")
	if err := c.AddQuota(8, 0); err == nil {
		t.Error("a zero award must be rejected locally rather than sent")
	}
}

// The machine-readable code must survive into the error, because it is what
// tells the operator what is actually wrong: AUTH_SESSION_LIMIT (the account's
// active-session cap is full) needs a different response than a bad password.
func TestLoginSurfacesErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"AUTH_SESSION_LIMIT","message":"Conflict","success":false}`))
	}))
	defer srv.Close()

	err := New(srv.URL).Login("napcat", "pw")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "AUTH_SESSION_LIMIT") {
		t.Errorf("error must name the code, got %v", err)
	}
}
