package napcat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// bridgeEnvelope wraps a OneBot envelope the way NapCat's WebUI debug bridge
// does: the bridge answers with its own {code,message,data} envelope and the
// adapter's reply sits inside data. These wrappers therefore have to unwrap
// both layers and must still surface the inner error.
func bridgeEnvelope(onebot string) string {
	return `{"code":0,"message":"success","data":` + onebot + `}`
}

// bridgeCall is the request the debug bridge receives.
type bridgeCall struct {
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
}

// bridgeServer serves /api/Debug/call, records the call and answers with the
// given OneBot envelope nested in the bridge's own envelope.
func bridgeServer(t *testing.T, onebot string, got *bridgeCall) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/Debug/call" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, got)
		fmt.Fprint(w, bridgeEnvelope(onebot))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The bridge is the path that still works when the QQ login has expired and the
// OneBot adapter answers errors, so it must forward the group id, decode the
// roster out of both envelopes, and pass the adapter's refusal through rather
// than turning it into an empty roster ("the group has no members").
func TestWebUIBridgeGetGroupMemberList(t *testing.T) {
	var got bridgeCall
	srv := bridgeServer(t, onebotResp(0, []map[string]any{
		{"user_id": 54321, "nickname": "alice", "card": "爱丽丝"},
		{"user_id": 99999, "nickname": "bob", "card": ""},
	}, ""), &got)

	c := NewWebUI(srv.URL, "t")
	c.SetCredential("cred")
	ms, err := c.GetGroupMemberList(1129535731)
	if err != nil {
		t.Fatalf("GetGroupMemberList: %v", err)
	}
	if got.Action != "get_group_member_list" {
		t.Errorf("action = %q", got.Action)
	}
	if id, _ := got.Params["group_id"].(float64); int64(id) != 1129535731 {
		t.Errorf("group_id not forwarded: %+v", got.Params)
	}
	if len(ms) != 2 || ms[0].QQ != 54321 || ms[0].DisplayName() != "爱丽丝" || ms[1].DisplayName() != "bob" {
		t.Errorf("roster not decoded through the bridge: %+v", ms)
	}

	fail := bridgeServer(t, onebotResp(1404, nil, "不支持的API get_group_member_list"), &bridgeCall{})
	ms, err = NewWebUI(fail.URL, "t").GetGroupMemberList(1129535731)
	if err == nil || ms != nil {
		t.Fatalf("adapter refusal swallowed: ms=%v err=%v", ms, err)
	}
}

// A board read through the bridge must decode today's page, and a group without
// a board must keep its sentinel: the sweep ignores ErrNoSigninBoard but must
// still report real failures.
func TestWebUIBridgeGetGroupSignedList(t *testing.T) {
	var got bridgeCall
	srv := bridgeServer(t, onebotResp(0, []map[string]any{
		{"user_id": 54321, "nick": "爱丽丝", "time": 1791630000, "rank": 1.5},
	}, ""), &got)

	c := NewWebUI(srv.URL, "t")
	c.SetCredential("cred")
	ss, err := c.GetGroupSignedList(1129535731)
	if err != nil {
		t.Fatalf("GetGroupSignedList: %v", err)
	}
	if got.Action != "get_group_signed_list" {
		t.Errorf("action = %q", got.Action)
	}
	if id, _ := got.Params["group_id"].(float64); int64(id) != 1129535731 {
		t.Errorf("group_id not forwarded: %+v", got.Params)
	}
	if len(ss) != 1 || ss[0].QQ != 54321 || ss[0].Rank != 1.5 || ss[0].Time != 1791630000 {
		t.Errorf("board not decoded through the bridge: %+v", ss)
	}

	fail := bridgeServer(t, onebotResp(1200, nil, "无法获取该群组打卡列表"), &bridgeCall{})
	ss, err = NewWebUI(fail.URL, "t").GetGroupSignedList(1129535731)
	if !errors.Is(err, ErrNoSigninBoard) {
		t.Fatalf("the boardless-group sentinel must survive the bridge: ss=%v err=%v", ss, err)
	}
}

// GetLoginInfo is the liveness probe: it must carry the decoded identity, and a
// dead bridge must not answer with a zero-valued account that reads as alive.
func TestWebUIBridgeGetLoginInfo(t *testing.T) {
	var got bridgeCall
	srv := bridgeServer(t, onebotResp(0, map[string]any{"user_id": 12345678, "nickname": "机器人"}, ""), &got)

	c := NewWebUI(srv.URL, "t")
	c.SetCredential("cred")
	li, err := c.GetLoginInfo()
	if err != nil {
		t.Fatalf("GetLoginInfo: %v", err)
	}
	if got.Action != "get_login_info" {
		t.Errorf("action = %q", got.Action)
	}
	if li == nil || li.UserID != 12345678 || li.Nickname != "机器人" {
		t.Errorf("identity not decoded through the bridge: %+v", li)
	}

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()
	li, err = NewWebUI(dead.URL, "t").GetLoginInfo()
	if err == nil || li != nil {
		t.Fatalf("a failed bridge call must not report a login: li=%v err=%v", li, err)
	}
}

// The OneBot client's own liveness probe. The identity is what the panel shows,
// and an error envelope ("登录态已失效") must not read as a live login.
func TestOneBotGetLoginInfo(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, onebotResp(0, map[string]any{"user_id": 12345678, "nickname": "机器人"}, ""))
	}))
	defer srv.Close()

	li, err := New(srv.URL, "tok").GetLoginInfo()
	if err != nil {
		t.Fatalf("GetLoginInfo: %v", err)
	}
	if gotPath != "/get_login_info" {
		t.Errorf("path = %q, want the get_login_info action", gotPath)
	}
	if li == nil || li.UserID != 12345678 || li.Nickname != "机器人" {
		t.Errorf("identity not decoded: %+v", li)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, onebotResp(1404, nil, "登录态已失效，请重新登录"))
	}))
	defer bad.Close()
	li, err = New(bad.URL, "").GetLoginInfo()
	if err == nil || li != nil {
		t.Fatalf("an expired login must not read as alive: li=%v err=%v", li, err)
	}
}
