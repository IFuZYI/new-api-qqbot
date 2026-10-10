package napcat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// onebotResp wraps a payload the way NapCat's OneBot adapter does.
func onebotResp(retcode int, data any, message string) string {
	b, _ := json.Marshal(map[string]any{
		"status": "ok", "retcode": retcode, "data": data, "message": message, "wording": "",
	})
	return string(b)
}

func TestCallSendsActionAndToken(t *testing.T) {
	var gotPath, gotAuth string
	var gotParams map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotParams)
		_, _ = w.Write([]byte(onebotResp(0, []any{}, "")))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok-abc")
	var out []Group
	if err := c.Call("get_group_list", map[string]any{"no_cache": true}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if gotPath != "/get_group_list" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotParams["no_cache"] != true {
		t.Errorf("params not sent: %+v", gotParams)
	}
}

// retcode 0 with an empty list is a valid answer, not a failure: it means the
// bot account is in no groups.
func TestEmptyGroupListIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(onebotResp(0, []any{}, "")))
	}))
	defer srv.Close()

	gs, err := New(srv.URL, "").GetGroupList()
	if err != nil {
		t.Fatalf("GetGroupList: %v", err)
	}
	if len(gs) != 0 {
		t.Errorf("want 0 groups, got %d", len(gs))
	}
}

func TestNonZeroRetcodeIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(onebotResp(1404, nil, "不支持的API get_group_list")))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "").GetGroupList()
	if err == nil {
		t.Fatal("expected retcode 1404 to be an error")
	}
	if !strings.Contains(err.Error(), "1404") {
		t.Errorf("error should name the retcode, got %v", err)
	}
}

func TestGetGroupListParsesGroups(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(onebotResp(0, []map[string]any{
			{"group_id": 1129535731, "group_name": "FuZY、ラッキー、fuzy", "member_count": 3, "max_member_count": 200},
		}, "")))
	}))
	defer srv.Close()

	gs, err := New(srv.URL, "").GetGroupList()
	if err != nil {
		t.Fatalf("GetGroupList: %v", err)
	}
	if len(gs) != 1 || gs[0].GroupID != 1129535731 || gs[0].MemberCount != 3 {
		t.Errorf("unexpected: %+v", gs)
	}
}

// A group card overrides the account nickname, which is what the operator sees.
func TestGetGroupMemberListPrefersCard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(onebotResp(0, []map[string]any{
			{"user_id": 54321, "nickname": "alice", "card": "爱丽丝"},
			{"user_id": 99999, "nickname": "bob", "card": ""},
		}, "")))
	}))
	defer srv.Close()

	ms, err := New(srv.URL, "").GetGroupMemberList(1129535731)
	if err != nil {
		t.Fatalf("GetGroupMemberList: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("want 2 members, got %d", len(ms))
	}
	if ms[0].QQ != 54321 || ms[0].DisplayName() != "爱丽丝" {
		t.Errorf("card not preferred: %+v", ms[0])
	}
	if ms[1].DisplayName() != "bob" {
		t.Errorf("nickname fallback broken: %+v", ms[1])
	}
}

// get_group_signed_list throws when the group has no board; that must surface
// as an error rather than a silent empty result, or a group that simply has
// check-in disabled would look like "nobody signed in".
func TestGetGroupSignedListSurfacesBoardError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(onebotResp(1200, nil, "无法获取该群组打卡列表")))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "").GetGroupSignedList(1129535731)
	if err == nil {
		t.Fatal("expected an error when the board cannot be read")
	}
	// A boardless group is normal, so it is reported as a distinct sentinel the
	// caller can recognise and ignore - not as a generic failure.
	if !errors.Is(err, ErrNoSigninBoard) {
		t.Errorf("want ErrNoSigninBoard, got %v", err)
	}
}

func TestGetGroupSignedListParsesBoard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(onebotResp(0, []map[string]any{
			{"user_id": 54321, "nick": "爱丽丝", "time": 1791630000, "rank": 1},
		}, "")))
	}))
	defer srv.Close()

	ss, err := New(srv.URL, "").GetGroupSignedList(1129535731)
	if err != nil {
		t.Fatalf("GetGroupSignedList: %v", err)
	}
	if len(ss) != 1 || ss[0].QQ != 54321 || ss[0].Rank != 1 || ss[0].Time != 1791630000 {
		t.Errorf("unexpected: %+v", ss)
	}
}

// ---------- WebUI bridge ----------

func TestWebUILoginHashesTokenAndStoresCredential(t *testing.T) {
	const webuiToken = "test-webui-token-not-a-real-secret"
	var gotHash string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var body map[string]string
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotHash = body["hash"]
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"Credential":"cred-xyz"}}`))
	}))
	defer srv.Close()

	c := NewWebUI(srv.URL, webuiToken)
	if err := c.Login(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	sum := sha256.Sum256([]byte(webuiToken + ".napcat"))
	if gotHash != hex.EncodeToString(sum[:]) {
		t.Errorf("wrong hash sent:\n got %s\nwant %s", gotHash, hex.EncodeToString(sum[:]))
	}
	if c.Credential() != "cred-xyz" {
		t.Errorf("credential not stored: %q", c.Credential())
	}
}

func TestWebUILoginSurfacesRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":-1,"message":"Unauthorized","data":null}`))
	}))
	defer srv.Close()

	if err := NewWebUI(srv.URL, "bad").Login(); err == nil {
		t.Fatal("expected a rejected login to error")
	}
}

// The WebUI's debug bridge forwards an arbitrary OneBot action to the live
// adapter, which is how actions are called without enabling a network adapter.
func TestWebUICallActionUsesBridge(t *testing.T) {
	var gotAction string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/Debug/call" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer cred-xyz" {
			t.Errorf("credential not attached: %q", r.Header.Get("Authorization"))
		}
		var body struct {
			Action string         `json:"action"`
			Params map[string]any `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		gotAction = body.Action
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"status":"ok","retcode":0,"data":[{"group_id":1129535731,"group_name":"g","member_count":3}]}}`))
	}))
	defer srv.Close()

	c := NewWebUI(srv.URL, "t")
	c.SetCredential("cred-xyz")
	gs, err := c.GetGroupList()
	if err != nil {
		t.Fatalf("GetGroupList: %v", err)
	}
	if gotAction != "get_group_list" {
		t.Errorf("action = %q", gotAction)
	}
	if len(gs) != 1 || gs[0].GroupID != 1129535731 {
		t.Errorf("unexpected: %+v", gs)
	}
}

func TestWebUIGetAndSetOneBotConfig(t *testing.T) {
	var setBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/OB11Config/GetConfig":
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"network":{"httpServers":[],"websocketServers":[]}}}`))
		case "/api/OB11Config/SetConfig":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &setBody)
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":null}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewWebUI(srv.URL, "t")
	c.SetCredential("cred")

	cfg, err := c.GetOneBotConfig()
	if err != nil {
		t.Fatalf("GetOneBotConfig: %v", err)
	}
	if _, ok := cfg.Network["websocketServers"]; !ok {
		t.Errorf("config not parsed: %+v", cfg.Network)
	}

	if err := c.SetOneBotConfig(cfg); err != nil {
		t.Fatalf("SetOneBotConfig: %v", err)
	}
	if setBody["config"] == "" {
		t.Error("SetConfig must send the config as a JSON string, not an object")
	}
}

// An operator pastes the address they see in the browser (the panel at
// .../webui/), but NapCat's API is at the root. The path must be dropped or
// every call hits the SPA's HTML instead of JSON.
func TestNormalizeWebUIURLStripsPath(t *testing.T) {
	cases := map[string]string{
		"http://localhost:6099/webui/":  "http://localhost:6099",
		"http://127.0.0.1:6099/webui":   "http://127.0.0.1:6099",
		"http://127.0.0.1:6099/":        "http://127.0.0.1:6099",
		"http://127.0.0.1:6099":         "http://127.0.0.1:6099",
		"127.0.0.1:6099/webui/":         "http://127.0.0.1:6099",
		"https://napcat.example.com/w/": "https://napcat.example.com",
	}
	for in, want := range cases {
		if got := NormalizeWebUIURL(in); got != want {
			t.Errorf("NormalizeWebUIURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeWebUIURL(""); got != "" {
		t.Errorf("empty should stay empty, got %q", got)
	}
}

func TestWebUIHostExtractsHostname(t *testing.T) {
	cases := map[string]string{
		"http://localhost:6099/webui/": "127.0.0.1",
		"http://127.0.0.1:6099":        "127.0.0.1",
		"napcat.internal:6099":         "napcat.internal",
		"http://[::1]:6099":            "::1",
		"":                             "127.0.0.1",
	}
	for in, want := range cases {
		if got := WebUIHost(in); got != want {
			t.Errorf("WebUIHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// The OneBot adapters bind on IPv4 loopback, but localhost commonly resolves to
// ::1 first, which cannot reach a 127.0.0.1 listener. Building the OneBot URLs
// from "localhost" therefore produces a URL that fails with connection refused
// even though the adapter is running, so the name is mapped to 127.0.0.1.
func TestEnsureAdaptersMapsLocalhostToIPv4(t *testing.T) {
	var sent struct {
		Network map[string][]map[string]any `json:"network"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/OB11Config/GetConfig":
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"network":{"httpServers":[],"websocketServers":[]}}}`))
		case "/api/OB11Config/SetConfig":
			var body map[string]string
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			_ = json.Unmarshal([]byte(body["config"]), &sent)
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":null}`))
		}
	}))
	defer srv.Close()

	c := NewWebUI(srv.URL, "t")
	c.SetCredential("cred")
	res, err := c.EnsureAdapters(AdapterOptions{ConnectHost: WebUIHost("http://localhost:6099/webui/")})
	if err != nil {
		t.Fatalf("EnsureAdapters: %v", err)
	}
	if res.OneBotURL != "http://127.0.0.1:3002" {
		t.Errorf("onebot url = %q, want the IPv4 form", res.OneBotURL)
	}
	if res.EventWSURL != "ws://127.0.0.1:3001" {
		t.Errorf("event ws url = %q, want the IPv4 form", res.EventWSURL)
	}
}

// EnsureAdapters must turn on both adapters in one write, preserve adapters it
// does not own, and report the URLs the caller stores.
func TestEnsureAdaptersWritesBothAdapters(t *testing.T) {
	var setConfig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/OB11Config/GetConfig":
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"network":{` +
				`"httpServers":[],"websocketServers":[],` +
				`"websocketClients":[{"name":"someone-elses","enable":true,"url":"ws://x"}]}}}`))
		case "/api/OB11Config/SetConfig":
			var body map[string]string
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			setConfig = body["config"]
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":null}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := NewWebUI(srv.URL, "t")
	c.SetCredential("cred")

	res, err := c.EnsureAdapters(AdapterOptions{ConnectHost: "127.0.0.1"})
	if err != nil {
		t.Fatalf("EnsureAdapters: %v", err)
	}
	if res.Token == "" || len(res.Token) != 32 {
		t.Errorf("expected a 32-hex token, got %q", res.Token)
	}
	if res.OneBotURL != "http://127.0.0.1:3002" {
		t.Errorf("onebot url = %q", res.OneBotURL)
	}
	if res.EventWSURL != "ws://127.0.0.1:3001" {
		t.Errorf("event ws url = %q", res.EventWSURL)
	}

	var sent struct {
		Network map[string][]map[string]any `json:"network"`
	}
	if err := json.Unmarshal([]byte(setConfig), &sent); err != nil {
		t.Fatalf("sent config is not JSON: %v\n%s", err, setConfig)
	}
	http := sent.Network["httpServers"]
	ws := sent.Network["websocketServers"]
	if len(http) != 1 || http[0]["name"] != HTTPAdapterName || http[0]["enable"] != true {
		t.Errorf("http adapter not enabled: %+v", http)
	}
	if http[0]["token"] != res.Token {
		t.Errorf("http adapter token mismatch: %+v", http[0])
	}
	if len(ws) != 1 || ws[0]["name"] != WSAdapterName || ws[0]["enable"] != true {
		t.Errorf("ws adapter not enabled: %+v", ws)
	}
	if ws[0]["enableForcePushEvent"] != true {
		t.Errorf("ws adapter must push events: %+v", ws[0])
	}
	// The operator's own adapter must survive.
	if len(sent.Network["websocketClients"]) != 1 {
		t.Errorf("an unrelated adapter was dropped: %+v", sent.Network["websocketClients"])
	}
}

// A re-run must not rotate the token out from under a live client, and must not
// duplicate the adapters.
func TestEnsureAdaptersReusesTokenAndIsIdempotent(t *testing.T) {
	const existing = "test-onebot-token-not-a-real-secret"
	var sent struct {
		Network map[string][]map[string]any `json:"network"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/OB11Config/GetConfig":
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"network":{` +
				`"httpServers":[{"name":"` + HTTPAdapterName + `","enable":true,"port":3002,"host":"127.0.0.1","token":"` + existing + `"}],` +
				`"websocketServers":[{"name":"` + WSAdapterName + `","enable":true,"host":"127.0.0.1","port":3001,"token":"` + existing + `"}]}}}`))
		case "/api/OB11Config/SetConfig":
			var body map[string]string
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			_ = json.Unmarshal([]byte(body["config"]), &sent)
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":null}`))
		}
	}))
	defer srv.Close()

	c := NewWebUI(srv.URL, "t")
	c.SetCredential("cred")

	res, err := c.EnsureAdapters(AdapterOptions{ConnectHost: "localhost"})
	if err != nil {
		t.Fatalf("EnsureAdapters: %v", err)
	}
	if res.Token != existing {
		t.Errorf("token rotated: got %q, want %q", res.Token, existing)
	}
	if n := len(sent.Network["httpServers"]); n != 1 {
		t.Errorf("http adapters duplicated: %d", n)
	}
	if n := len(sent.Network["websocketServers"]); n != 1 {
		t.Errorf("ws adapters duplicated: %d", n)
	}
	// The reported URL uses the host the operator gave, not the bind host.
	if res.OneBotURL != "http://localhost:3002" {
		t.Errorf("onebot url = %q", res.OneBotURL)
	}
}

// EnsureAdapters must not trample a custom port the operator already set on our
// own adapter... unless they asked for one; here we pass a port and expect it.
func TestEnsureAdaptersHonoursRequestedPorts(t *testing.T) {
	var sent struct {
		Network map[string][]map[string]any `json:"network"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/OB11Config/GetConfig":
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"network":{"httpServers":[],"websocketServers":[]}}}`))
		case "/api/OB11Config/SetConfig":
			var body map[string]string
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			_ = json.Unmarshal([]byte(body["config"]), &sent)
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":null}`))
		}
	}))
	defer srv.Close()

	c := NewWebUI(srv.URL, "t")
	c.SetCredential("cred")
	res, err := c.EnsureAdapters(AdapterOptions{ConnectHost: "127.0.0.1", HTTPPort: 4002, WSPort: 4001})
	if err != nil {
		t.Fatalf("EnsureAdapters: %v", err)
	}
	if sent.Network["httpServers"][0]["port"] != float64(4002) {
		t.Errorf("http port not applied: %+v", sent.Network["httpServers"][0])
	}
	if sent.Network["websocketServers"][0]["port"] != float64(4001) {
		t.Errorf("ws port not applied: %+v", sent.Network["websocketServers"][0])
	}
	if res.OneBotURL != "http://127.0.0.1:4002" {
		t.Errorf("onebot url = %q", res.OneBotURL)
	}
}
