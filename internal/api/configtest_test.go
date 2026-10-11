package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/napcat"
	"qqbot/internal/newapi"
	"qqbot/internal/service"
	"qqbot/internal/store"
)

// fakeProber records what it was asked to probe and returns canned outcomes, so
// the endpoints are testable without live systems.
type fakeProber struct {
	gatewayUser *newapi.User
	gatewayErr  error
	webuiInfo   *napcat.LoginInfo
	webuiErr    error
	onebotInfo  *napcat.LoginInfo
	onebotErr   error
	eventWSErr  error

	gotGatewayURL, gotGatewayUser, gotGatewayPass string
	gotWebUIURL, gotWebUIToken                    string
	gotOneBotURL, gotOneBotToken                  string
	gotEventWSURL, gotEventWSToken                string
}

func (f *fakeProber) ProbeGateway(url, username, password string) (*newapi.User, error) {
	f.gotGatewayURL, f.gotGatewayUser, f.gotGatewayPass = url, username, password
	return f.gatewayUser, f.gatewayErr
}

func (f *fakeProber) ProbeWebUI(url, token string) (*napcat.LoginInfo, error) {
	f.gotWebUIURL, f.gotWebUIToken = url, token
	return f.webuiInfo, f.webuiErr
}

func (f *fakeProber) ProbeOneBot(url, token string) (*napcat.LoginInfo, error) {
	f.gotOneBotURL, f.gotOneBotToken = url, token
	return f.onebotInfo, f.onebotErr
}

func (f *fakeProber) ProbeEventWS(url, token string) error {
	f.gotEventWSURL, f.gotEventWSToken = url, token
	return f.eventWSErr
}

// newProbeEnv builds an authenticated server with a prober installed.
func newProbeEnv(t *testing.T, p Prober) (*httptest.Server, *http.Client, *config.Manager) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := service.New(st, fakeGateway{}, fakeQQ{})
	cfgMgr := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)
	r := NewRouter(st, svc, cfgMgr, t.TempDir())
	r.SetProber(p)
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar}
	resp, err := cl.Post(srv.URL+"/api/auth/setup", "application/json",
		strings.NewReader(`{"username":"admin","password":"test-password"}`))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	resp.Body.Close()
	return srv, cl, cfgMgr
}

type probeResponse struct {
	Results []checkResult `json:"results"`
}

// A successful gateway test reports which account answered, not just "ok".
func TestTestNewAPIReportsIdentity(t *testing.T) {
	fake := &fakeProber{gatewayUser: &newapi.User{
		ID: 3, Username: "napcat", DisplayName: "管理", Role: 10, Group: "authorized",
	}}
	srv, cl, _ := newProbeEnv(t, fake)

	var got probeResponse
	code := postJSONWith(t, cl, srv.URL+"/api/config/test/newapi", map[string]any{
		"url": "https://api.fuzy.site", "username": "napcat", "password": "pw",
	}, &got)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Results) != 1 || !got.Results[0].OK {
		t.Fatalf("expected one passing result: %+v", got.Results)
	}
	detail := got.Results[0].Detail
	for _, want := range []string{"napcat", "管理", "id=3", "role=10", "authorized"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q should mention %q", detail, want)
		}
	}
	if fake.gotGatewayURL != "https://api.fuzy.site" || fake.gotGatewayPass != "pw" {
		t.Errorf("probe got %q/%q", fake.gotGatewayURL, fake.gotGatewayPass)
	}
}

// A rejected login is a 200 with a failed check, not an HTTP error: the test
// ran and produced an answer, and the UI renders it as a row.
func TestTestNewAPIFailureIsReportedInTheBody(t *testing.T) {
	fake := &fakeProber{gatewayErr: errors.New("AUTH_INVALID: 用户名或密码错误")}
	srv, cl, _ := newProbeEnv(t, fake)

	var got probeResponse
	code := postJSONWith(t, cl, srv.URL+"/api/config/test/newapi", map[string]any{
		"url": "https://api.fuzy.site", "username": "napcat", "password": "bad",
	}, &got)
	if code != 200 {
		t.Fatalf("a failed check must still be a 200, got %d", code)
	}
	if len(got.Results) != 1 || got.Results[0].OK {
		t.Fatalf("expected a failing result: %+v", got.Results)
	}
	if !strings.Contains(got.Results[0].Error, "AUTH_INVALID") {
		t.Errorf("error should carry the gateway's code: %q", got.Results[0].Error)
	}
}

// The form shows the redaction placeholder for a saved password; testing must
// use the stored secret rather than the literal placeholder.
func TestTestNewAPIUsesStoredPasswordWhenRedacted(t *testing.T) {
	fake := &fakeProber{gatewayUser: &newapi.User{ID: 3, Username: "napcat", Role: 10}}
	srv, cl, cfgMgr := newProbeEnv(t, fake)

	if err := cfgMgr.Save(config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "real-pw"},
		NapCat: config.NapCat{OneBotURL: "http://127.0.0.1:3002"},
	}); err != nil {
		t.Fatal(err)
	}

	postJSONWith(t, cl, srv.URL+"/api/config/test/newapi", map[string]any{
		"url": "https://api.fuzy.site", "username": "napcat", "password": config.RedactedPassword,
	}, nil)

	if fake.gotGatewayPass != "real-pw" {
		t.Errorf("the stored password should be used, got %q", fake.gotGatewayPass)
	}
}

// A blank URL with nothing stored is a client error, not a probe.
func TestTestNewAPIRequiresCredentials(t *testing.T) {
	srv, cl, _ := newProbeEnv(t, &fakeProber{})
	code := postJSONWith(t, cl, srv.URL+"/api/config/test/newapi", map[string]any{}, nil)
	if code != 400 {
		t.Errorf("expected 400 for an empty form, got %d", code)
	}
}

// Each NapCat transport is probed and reported separately, so a broken hop is
// named instead of collapsing into one "cannot connect".
func TestTestNapCatProbesEachTransportSeparately(t *testing.T) {
	fake := &fakeProber{
		webuiInfo:  &napcat.LoginInfo{UserID: 3688665216, Nickname: "fuzy"},
		onebotErr:  errors.New("dial tcp 127.0.0.1:3002: connect: connection refused"),
		eventWSErr: nil,
	}
	srv, cl, _ := newProbeEnv(t, fake)

	var got probeResponse
	code := postJSONWith(t, cl, srv.URL+"/api/config/test/napcat", map[string]any{
		"webui_url": "http://127.0.0.1:6099", "webui_token": "wt",
		"onebot_url": "http://127.0.0.1:3002", "onebot_token": "ot",
		"event_ws_url": "ws://127.0.0.1:3001", "event_ws_token": "ot",
	}, &got)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Results) != 3 {
		t.Fatalf("want 3 results (webui, onebot, event), got %d: %+v", len(got.Results), got.Results)
	}

	byName := map[string]checkResult{}
	for _, r := range got.Results {
		byName[r.Name] = r
	}

	if w := byName["NapCat WebUI"]; !w.OK {
		t.Errorf("webui should pass: %+v", w)
	} else if !strings.Contains(w.Detail, "3688665216") {
		t.Errorf("webui detail should name the bot: %q", w.Detail)
	}
	if ob := byName["OneBot HTTP"]; ob.OK {
		t.Error("onebot should be reported as failing")
	} else if !strings.Contains(ob.Error, "connection refused") {
		t.Errorf("onebot error should carry the reason: %q", ob.Error)
	}
	if ev := byName["事件 WebSocket"]; !ev.OK {
		t.Errorf("the event socket should pass: %+v", ev)
	}
}

// An unconfigured transport is skipped, not failed: without an event socket the
// service still works (ADR-0003), so calling it a failure would be misleading.
func TestTestNapCatSkipsUnconfiguredTransports(t *testing.T) {
	fake := &fakeProber{onebotInfo: &napcat.LoginInfo{UserID: 123, Nickname: "bot"}}
	srv, cl, _ := newProbeEnv(t, fake)

	var got probeResponse
	postJSONWith(t, cl, srv.URL+"/api/config/test/napcat", map[string]any{
		"onebot_url": "http://127.0.0.1:3002", "onebot_token": "ot",
	}, &got)

	if len(got.Results) != 3 {
		t.Fatalf("want 3 results, got %d", len(got.Results))
	}
	var webui, event *checkResult
	for i := range got.Results {
		switch got.Results[i].Name {
		case "NapCat WebUI":
			webui = &got.Results[i]
		case "事件 WebSocket":
			event = &got.Results[i]
		}
	}
	if webui == nil || !webui.Skipped {
		t.Errorf("an unconfigured WebUI should be skipped: %+v", webui)
	}
	if event == nil || !event.Skipped {
		t.Errorf("an unconfigured event socket should be skipped: %+v", event)
	}
}

// A WebUI that accepts the credential but has no QQ logged in is a partial
// success: the panel and token are right, the bot simply is not online.
func TestTestNapCatWebUIWithoutQQLogin(t *testing.T) {
	// ProbeWebUI reports (nil, nil) for "logged in to the panel, no QQ".
	fake := &fakeProber{webuiInfo: nil, webuiErr: nil, onebotInfo: &napcat.LoginInfo{UserID: 1}}
	srv, cl, _ := newProbeEnv(t, fake)

	var got probeResponse
	postJSONWith(t, cl, srv.URL+"/api/config/test/napcat", map[string]any{
		"webui_url": "http://127.0.0.1:6099", "webui_token": "wt",
		"onebot_url": "http://127.0.0.1:3002",
	}, &got)

	for _, r := range got.Results {
		if r.Name == "NapCat WebUI" {
			if !r.OK {
				t.Error("a working panel must pass even when QQ is offline")
			}
			if !strings.Contains(r.Detail, "QQ 未登录") {
				t.Errorf("detail should explain the missing bot: %q", r.Detail)
			}
		}
	}
}

// With no prober installed the endpoints say testing is unavailable rather than
// reporting a bogus failure.
func TestTestEndpointsWithoutProber(t *testing.T) {
	srv, _, cl := newTestServer(t)
	for _, path := range []string{"/api/config/test/newapi", "/api/config/test/napcat"} {
		code := postJSONWith(t, cl, srv.URL+path, map[string]any{
			"url": "https://x", "username": "u", "password": "p",
			"onebot_url": "http://127.0.0.1:3002",
		}, nil)
		if code != http.StatusNotImplemented {
			t.Errorf("%s: expected 501, got %d", path, code)
		}
	}
}

// The tests must not leak the stored secret back to the browser.
func TestTestEndpointsDoNotEchoSecrets(t *testing.T) {
	fake := &fakeProber{
		gatewayUser: &newapi.User{ID: 3, Username: "napcat", Role: 10},
		webuiInfo:   &napcat.LoginInfo{UserID: 1},
		onebotInfo:  &napcat.LoginInfo{UserID: 1},
	}
	srv, cl, cfgMgr := newProbeEnv(t, fake)
	if err := cfgMgr.Save(config.Config{
		NewAPI: config.NewAPI{URL: "https://api.fuzy.site", Username: "napcat", Password: "secret-gw-pw"},
		NapCat: config.NapCat{
			WebUIURL: "http://127.0.0.1:6099", WebUIToken: "secret-webui-tok",
			OneBotURL: "http://127.0.0.1:3002", OneBotToken: "secret-ob-tok",
		},
	}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/config/test/newapi", "/api/config/test/napcat"} {
		body := postRaw(t, cl, srv.URL+path, map[string]any{})
		for _, secret := range []string{"secret-gw-pw", "secret-webui-tok", "secret-ob-tok"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s echoed the secret %q", path, secret)
			}
		}
	}
}

// postRaw returns the raw response body, so a test can assert on what actually
// crossed the wire rather than on a decoded view.
func postRaw(t *testing.T, c *http.Client, url string, body any) string {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := c.Post(url, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}
