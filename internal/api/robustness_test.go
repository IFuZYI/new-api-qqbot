package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDecodeRejectsOversizedBody proves the body cap is real: a >1 MiB body must
// be refused with the readable error, and a normal body must still decode.
func TestDecodeRejectsOversizedBody(t *testing.T) {
	// Comfortably over the cap.
	huge := `{"blob":"` + strings.Repeat("x", maxBodyBytes+1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(huge))
	var out map[string]any
	err := decode(req, &out)
	if err == nil {
		t.Fatal("an oversized body must be refused, got nil error")
	}
	var tooLarge *bodyTooLargeError
	if !strings.Contains(err.Error(), "请求体过大") {
		t.Fatalf("expected the readable body-too-large error, got: %v", err)
	}
	_ = tooLarge

	// And the normal path still works.
	ok := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"a"}`))
	var got map[string]any
	if err := decode(ok, &got); err != nil {
		t.Fatalf("a normal body must decode: %v", err)
	}
	if got["username"] != "a" {
		t.Fatalf("decoded wrong payload: %+v", got)
	}
}

// TestRecoverPanicsReturns500 proves a panicking handler yields a 500 response
// rather than a dropped connection.
//
// It drives Router.Handler(), not recoverPanics() directly: the wiring is the
// thing that can be missing, and a test of the middleware in isolation stays
// green when Handler() forgets to wrap the mux (verified by reverting the wrap
// and watching this test fail).
func TestRecoverPanicsReturns500(t *testing.T) {
	r := &Router{mux: http.NewServeMux()}
	r.mux.HandleFunc("/api/x", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := r.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != 500 {
		t.Fatalf("want 500 from a panicking handler, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "服务器内部错误") {
		t.Fatalf("body should carry the generic message, got: %s", rec.Body.String())
	}
}
