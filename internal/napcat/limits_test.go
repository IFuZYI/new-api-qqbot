package napcat

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// oversizedHandler answers every request with a body one byte past the cap, so
// the client must refuse it. A body that exactly fills the cap is tested
// separately: it is a legitimate (if silly) response and must still decode.
func oversizedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.CopyN(w, byteRepeater('x'), maxResponseBody+1)
	}
}

// byteRepeater is an endless source of one byte. A handler that wrote a fixed
// buffer would return before the client stopped reading, and the test would not
// exercise "the cap stops the read" at all.
type byteRepeater byte

func (b byteRepeater) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// An upstream that answers with an unbounded body must not be read into memory.
// A misdirected URL or a hijacked response (an HTML error page from a proxy, the
// WebUI SPA served instead of JSON) can be arbitrarily large, and io.ReadAll
// would grow the process until it dies.
func TestCallRefusesOversizedResponseBody(t *testing.T) {
	srv := httptest.NewServer(oversizedHandler())
	defer srv.Close()

	_, err := New(srv.URL, "").GetGroupList()
	if err == nil {
		t.Fatal("an oversized response must be an error, not a silent read")
	}
	// The cause must be named: a bare decode error on a truncated body would
	// send the operator looking for a NapCat bug that is not there.
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", err)
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("the message must say the body was too large: %v", err)
	}
	if !strings.Contains(err.Error(), "get_group_list") {
		t.Errorf("the message must name the action that was refused: %v", err)
	}
}

// The WebUI bridge reads its own body and must be capped too, not just the
// OneBot path.
func TestWebUIDoRefusesOversizedResponseBody(t *testing.T) {
	srv := httptest.NewServer(oversizedHandler())
	defer srv.Close()

	w := NewWebUI(srv.URL, "t")
	w.SetCredential("cred")
	if err := w.Login(); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", err)
	}
}

// endlessRepeater never returns EOF, so a reader without a cap consumes memory
// until the process dies rather than stopping at a length check.
//
// This is the test that pins the cap itself. The finite oversizedHandler above
// proves the body is *refused*, but not that the read *stops*: replacing the
// LimitReader with a plain io.ReadAll still passes that test, because the test
// server's body ends on its own and the length check then fires. Only a source
// that never ends distinguishes a bounded read from an unbounded one.
type endlessRepeater struct{}

func (endlessRepeater) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestReadBodyStopsAtTheCap(t *testing.T) {
	// A bound far above the cap is fine; the assertion is that readBody returns
	// at all (an uncapped read would never return and the test would time out
	// rather than fail — so also assert the byte count).
	done := make(chan struct{})
	var (
		got []byte
		err error
	)
	go func() {
		defer close(done)
		got, err = readBody(endlessRepeater{})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("readBody did not return on an endless body: the read is not capped")
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", err)
	}
	// The cap is what bounds memory: at most one byte past it may be held.
	if len(got) > maxResponseBody+1 {
		t.Errorf("held %d bytes, want at most %d", len(got), maxResponseBody+1)
	}
}

// A body that fits must be unaffected: the cap may not turn an ordinary answer
// into an error, and a body exactly at the limit must not be mistaken for a
// truncated one (the read goes one byte past the cap to tell them apart).
func TestReadBodyAcceptsBodiesUpToTheCap(t *testing.T) {
	atCap := strings.Repeat("x", maxResponseBody)
	got, err := readBody(strings.NewReader(atCap))
	if err != nil {
		t.Fatalf("a body exactly at the cap must be read, got %v", err)
	}
	if len(got) != maxResponseBody {
		t.Errorf("read %d bytes, want %d", len(got), maxResponseBody)
	}

	onePast := strings.Repeat("x", maxResponseBody+1)
	if _, err := readBody(strings.NewReader(onePast)); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("a body one byte past the cap must be refused, got %v", err)
	}
}

// The cap must not disturb a normal call: a small, well-formed answer still
// decodes, and the empty-body case (a valid empty group list) still works.
func TestCallStillSucceedsUnderTheCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(onebotResp(0, []map[string]any{
			{"group_id": 1129535731, "group_name": "g", "member_count": 3},
		}, "")))
	}))
	defer srv.Close()

	gs, err := New(srv.URL, "").GetGroupList()
	if err != nil {
		t.Fatalf("a small response must be unaffected: %v", err)
	}
	if len(gs) != 1 || gs[0].GroupID != 1129535731 {
		t.Errorf("unexpected: %+v", gs)
	}
}

// ---------- Failover ----------

// A working primary must be used as-is: the fallback is a second path to the
// same answer, and calling it anyway would double every request against NapCat.
func TestFailoverUsesPrimaryWhenItSucceeds(t *testing.T) {
	fallbackCalled := false
	got, err := Failover(
		func() ([]Group, error) {
			return []Group{{GroupID: 1}}, nil
		},
		func() ([]Group, error) {
			fallbackCalled = true
			return []Group{{GroupID: 2}}, nil
		},
	)
	if err != nil {
		t.Fatalf("Failover: %v", err)
	}
	if fallbackCalled {
		t.Error("the fallback must not run when the primary succeeded")
	}
	if len(got) != 1 || got[0].GroupID != 1 {
		t.Errorf("want the primary's result, got %+v", got)
	}
}

// This is the failure the combinator exists for: NapCat's OneBot port is
// listening but QQ is not logged in, so the client builds fine and every call
// fails. The WebUI bridge must then answer.
func TestFailoverFallsBackWhenTheCallFails(t *testing.T) {
	got, err := Failover(
		func() ([]Group, error) {
			return nil, errors.New("get_group_list: retcode 1200: 登录态已失效，请重新登录")
		},
		func() ([]Group, error) {
			return []Group{{GroupID: 7}}, nil
		},
	)
	if err != nil {
		t.Fatalf("a failed primary must not surface when the fallback works: %v", err)
	}
	if len(got) != 1 || got[0].GroupID != 7 {
		t.Errorf("want the fallback's result, got %+v", got)
	}
}

// Both paths down is an operator-facing state: it must name both causes, or the
// one that happens to be reported last looks like the only problem.
func TestFailoverJoinsBothCauses(t *testing.T) {
	onebotErr := errors.New("get_group_list: dial tcp 127.0.0.1:3002: connection refused")
	webuiErr := errors.New("NapCat WebUI requires 2FA, which this service cannot satisfy")

	_, err := Failover(
		func() ([]Group, error) { return nil, onebotErr },
		func() ([]Group, error) { return nil, webuiErr },
	)
	if err == nil {
		t.Fatal("both paths failing must be an error")
	}
	if !errors.Is(err, onebotErr) {
		t.Errorf("the OneBot cause must survive in the error: %v", err)
	}
	if !errors.Is(err, webuiErr) {
		t.Errorf("the WebUI cause must survive in the error: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "connection refused") || !strings.Contains(msg, "2FA") {
		t.Errorf("both causes must be readable in the message: %q", msg)
	}
}

// Failover is generic so it can wrap every QQ read; the value on failure must
// not be a half-populated result a caller could mistake for data.
func TestFailoverReturnsNoValueOnDoubleFailure(t *testing.T) {
	got, err := Failover(
		func() ([]GroupMember, error) { return nil, errors.New("a") },
		func() ([]GroupMember, error) { return []GroupMember{{QQ: 1}}, errors.New("b") },
	)
	if err == nil {
		t.Fatal("want an error")
	}
	if got != nil {
		t.Errorf("a failed call must not hand back a value, got %+v", got)
	}
}

// Failover must work for a pointer-returning read too, which is how the login
// probe is shaped.
func TestFailoverWorksForPointerResults(t *testing.T) {
	li, err := Failover(
		func() (*LoginInfo, error) { return nil, errors.New("primary down") },
		func() (*LoginInfo, error) { return &LoginInfo{UserID: 3688665216}, nil },
	)
	if err != nil {
		t.Fatalf("Failover: %v", err)
	}
	if li == nil || li.UserID != 3688665216 {
		t.Errorf("unexpected: %+v", li)
	}
}

// A condition that is normal rather than a failure must stay recognisable
// through the join: a boardless group fails on both paths (the WebUI bridge
// drives the same NapCat action), and the caller's ignore-the-board logic reads
// it with errors.Is.
func TestFailoverPreservesSentinels(t *testing.T) {
	boardless := func() ([]SignedMember, error) {
		return nil, fmt.Errorf("get_group_signed_list: %w", ErrNoSigninBoard)
	}
	_, err := Failover(boardless, boardless)
	if !errors.Is(err, ErrNoSigninBoard) {
		t.Errorf("the sentinel must survive the join, got %v", err)
	}
}

// The real shape of the failure this exists for: the OneBot endpoint refuses
// (QQ logged out, adapter gone) while the WebUI bridge answers. Both are the
// production clients, against one stand-in NapCat.
func TestFailoverWithRealClientsFallsBackToWebUI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_group_list":
			// The OneBot adapter is listening but the QQ session is gone.
			_, _ = w.Write([]byte(onebotResp(1200, nil, "登录态已失效，请重新登录")))
		case "/api/Debug/call":
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"status":"ok","retcode":0,"data":[{"group_id":1129535731,"group_name":"g","member_count":3}]}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	ob := New(srv.URL, "")
	w := NewWebUI(srv.URL, "t")
	w.SetCredential("cred")

	gs, err := Failover(ob.GetGroupList, w.GetGroupList)
	if err != nil {
		t.Fatalf("the WebUI path must cover a logged-out OneBot: %v", err)
	}
	if len(gs) != 1 || gs[0].GroupID != 1129535731 {
		t.Errorf("unexpected: %+v", gs)
	}
}

// And when both are down, the error names both, so the operator does not chase
// only the one that was reported last.
func TestFailoverWithRealClientsReportsBothFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_group_list":
			_, _ = w.Write([]byte(onebotResp(1200, nil, "登录态已失效，请重新登录")))
		default:
			_, _ = w.Write([]byte(`{"code":-1,"message":"Not Login","data":null}`))
		}
	}))
	defer srv.Close()

	ob := New(srv.URL, "")
	w := NewWebUI(srv.URL, "t")
	w.SetCredential("cred")

	_, err := Failover(ob.GetGroupList, w.GetGroupList)
	if err == nil {
		t.Fatal("both paths down must be an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "登录态已失效") {
		t.Errorf("the OneBot cause must be in the message: %q", msg)
	}
	if !strings.Contains(msg, "Not Login") {
		t.Errorf("the WebUI cause must be in the message: %q", msg)
	}
}
