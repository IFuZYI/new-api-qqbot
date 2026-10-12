package api

import (
	"testing"

	"qqbot/internal/config"
	"qqbot/internal/newapi"
)

// Testing from the connection page usually means "try what is stored": the
// operator opens the card, the fields already show the stored values (the
// password as the redaction placeholder) and they press 测试.
//
// The URL and the password already fell back to the stored configuration, but
// the USERNAME did not: an empty body answered
// 400 「请先填写 new-api 地址、账号与密码」 even though every value the probe
// needed was stored, which is a misleading error on the most natural action on
// that page.
func TestGatewayTestFallsBackToTheStoredUsername(t *testing.T) {
	// The real prober never returns (nil, nil) — newapi's Self() yields a struct
	// whenever the error is nil — so the fake mirrors that contract.
	fake := &fakeProber{gatewayUser: &newapi.User{ID: 5, Username: "napcat", Role: 10}}
	srv, cl, cfg := newProbeEnv(t, fake)
	saveGatewayConfig(t, cfg, "napcat", "stored-password")

	var got probeResponse
	code := postJSONWith(t, cl, srv.URL+"/api/config/test/newapi", map[string]any{}, &got)
	if code != 200 {
		t.Fatalf("status %d, want 200 (everything needed is stored): %+v", code, got)
	}
	if len(got.Results) != 1 {
		t.Fatalf("results = %+v", got.Results)
	}
	if fake.gotGatewayUser != "napcat" {
		t.Errorf("the probe got username %q, want the stored %q", fake.gotGatewayUser, "napcat")
	}
	if fake.gotGatewayPass != "stored-password" {
		t.Errorf("the probe got the wrong password")
	}
}

// A body that overrides only some fields still works: each field falls back on
// its own.
func TestGatewayTestAcceptsAPartialBody(t *testing.T) {
	fake := &fakeProber{gatewayUser: &newapi.User{ID: 5, Username: "stored-user", Role: 10}}
	srv, cl, cfg := newProbeEnv(t, fake)
	saveGatewayConfig(t, cfg, "stored-user", "stored-password")

	var got probeResponse
	code := postJSONWith(t, cl, srv.URL+"/api/config/test/newapi",
		map[string]any{"username": "typed-user"}, &got)
	if code != 200 {
		t.Fatalf("status %d, want 200: %+v", code, got)
	}
	if fake.gotGatewayUser != "typed-user" {
		t.Errorf("the body's username must win, got %q", fake.gotGatewayUser)
	}
}

// With nothing stored and nothing supplied the refusal is still a 400: the
// fallback must not turn a genuinely empty configuration into a gateway call.
func TestGatewayTestRefusesWhenNothingIsConfigured(t *testing.T) {
	srv, cl, _ := newProbeEnv(t, &fakeProber{})

	var got map[string]any
	code := postJSONWith(t, cl, srv.URL+"/api/config/test/newapi", map[string]any{}, &got)
	if code != 400 {
		t.Errorf("status %d, want 400 with an empty store: %+v", code, got)
	}
}

// saveGatewayConfig stores a complete gateway configuration through the same
// manager the handler reads.
func saveGatewayConfig(t *testing.T, cfg *config.Manager, username, password string) {
	t.Helper()
	cur := cfg.MustLoad()
	cur.NewAPI = config.NewAPI{URL: "https://api.example.test", Username: username, Password: password}
	if err := cfg.Save(cur); err != nil {
		t.Fatalf("save config: %v", err)
	}
}
