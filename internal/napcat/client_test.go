package napcat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	if !strings.Contains(err.Error(), "打卡") {
		t.Errorf("error should carry the upstream message, got %v", err)
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
