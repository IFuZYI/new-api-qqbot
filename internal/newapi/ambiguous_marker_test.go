package newapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// AmbiguousWrite is the marker method that lets a caller tell "the gateway may
// already have credited this non-idempotent write" from "the gateway refused
// it". The distinction is load-bearing: the service keeps the claim held (and
// asks the operator to reconcile) only for an ambiguous outcome, while a
// definite refusal must stay retryable. A marker that answered false - or a
// refusal that carried it - would either pay twice or strand a legitimate retry.
func TestAmbiguousWriteMarkerDistinguishesRefusal(t *testing.T) {
	// The method itself: the type reports uncertainty.
	var e error = &AmbiguousWriteError{Err: errors.New("read response: unexpected EOF")}
	marker, ok := e.(interface{ AmbiguousWrite() bool })
	if !ok {
		t.Fatalf("%T does not carry the marker method", e)
	}
	if !marker.AmbiguousWrite() {
		t.Fatal("AmbiguousWriteError must report an ambiguous write")
	}
	if !IsAmbiguousWrite(e) {
		t.Fatal("the marker and IsAmbiguousWrite must agree")
	}

	// A definite refusal must NOT be classified ambiguous, or the claim would
	// never be released and the reward could never be retried.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"message":"insufficient permission"}`))
	}))
	defer srv.Close()
	refusal := New(srv.URL).AddQuota(9, 100)
	if refusal == nil {
		t.Fatal("a rejected write must be an error")
	}
	if IsAmbiguousWrite(refusal) {
		t.Fatalf("a definite refusal must not be ambiguous: %v", refusal)
	}
	if _, ok := refusal.(interface{ AmbiguousWrite() bool }); ok {
		t.Fatalf("a definite refusal must not carry the marker: %T", refusal)
	}
}
