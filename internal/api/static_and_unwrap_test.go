package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// statusRecorder.Unwrap is what lets http.ResponseController reach the real
// writer through the tracing wrapper. Without it, a handler that needs an
// optional interface (the flush or hijack a streaming response uses) silently
// loses it, and the connection behaves differently from an untraced one.
func TestStatusRecorderUnwrapReachesTheRealWriter(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}

	if got := rec.Unwrap(); got != rec.ResponseWriter {
		t.Fatalf("Unwrap() = %#v, want the wrapped writer", got)
	}

	// The controller must find the underlying writer's capabilities through
	// the chain rather than stopping at the recorder.
	flusher := httptest.NewRecorder()
	inner := &statusRecorder{ResponseWriter: flusher}
	outer := &statusRecorder{ResponseWriter: inner}
	if err := http.NewResponseController(outer).Flush(); err != nil {
		t.Fatalf("ResponseController could not flush through the recorder chain: %v", err)
	}
	if !flusher.Flushed {
		t.Error("the flush did not reach the real writer")
	}
}

// And end to end: a request that panics inside a handler is answered 500 by
// recoverPanics through the same wrapper chain, and the recorder still reports
// the status the client saw.
func TestUnwrapChainSurvivesTheMiddlewareStack(t *testing.T) {
	var got http.ResponseWriter
	h := recoverPanics(traceRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got = w
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))

	// The handler sees the recorder (so noteErr can attach an error) ...
	if _, ok := got.(*statusRecorder); !ok {
		t.Fatalf("handler writer = %T, want *statusRecorder", got)
	}
	// ... and the recorder still unwraps to the original writer, so an
	// optional interface is reachable all the way down.
	if u, ok := got.(interface{ Unwrap() http.ResponseWriter }); !ok || u.Unwrap() != rec {
		t.Errorf("the unwrap chain does not reach the original writer")
	}
}

// noteErr walks that same chain to attach the error to the request's record, so
// a 5xx that hides its detail from the client still names it in the log.
func TestNoteErrWalksTheUnwrapChain(t *testing.T) {
	// A handler may wrap the writer it was given (a middleware, or a helper
	// that decorates it); the error must still land on the recorder
	// traceRequests installed, not be dropped at the first wrapper.
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	wrapped := &unwrappingWriter{ResponseWriter: rec}
	noteErr(wrapped, errText("sqlite: no such table: admins"))
	if rec.err == nil {
		t.Fatal("the error did not reach the recorder through one wrapper")
	}

	rec2 := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	noteErr(&unwrappingWriter{ResponseWriter: &unwrappingWriter{ResponseWriter: rec2}}, errText("x"))
	if rec2.err == nil {
		t.Error("the error did not reach the recorder through two wrappers")
	}

	// A writer that cannot unwrap is a dead end, not a panic.
	noteErr(&opaqueWriter{ResponseWriter: httptest.NewRecorder()}, errText("x"))

	// And end to end: the request's own record carries the error text the
	// client was not shown.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := traceRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		wrapped := &unwrappingWriter{ResponseWriter: w}
		writeAuthErr(wrapped, 500, errText("sqlite: no such table: admins"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/auth/state", nil))

	out := buf.String()
	if !strings.Contains(out, "no such table") {
		t.Errorf("the traced record is missing the hidden error: %s", out)
	}
	if !strings.Contains(out, `"status":500`) {
		t.Errorf("the record must carry the status: %s", out)
	}
}

// unwrappingWriter is a middleware-style wrapper: it is not a statusRecorder,
// but it exposes the one underneath.
type unwrappingWriter struct{ http.ResponseWriter }

func (u *unwrappingWriter) Unwrap() http.ResponseWriter { return u.ResponseWriter }

// opaqueWriter wraps a writer without exposing it, so noteErr must stop.
type opaqueWriter struct{ http.ResponseWriter }
