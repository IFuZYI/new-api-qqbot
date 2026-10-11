package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"qqbot/internal/config"
	"qqbot/internal/napcat"
)

// checkResult is one transport's outcome. It always carries a name so the UI can
// label it, and a human-readable detail: on success the identity that answered,
// on failure the reason. Reporting the identity matters because "it works" is
// not useful when the credentials point at the wrong account.
type checkResult struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Error   string `json:"error,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

// testNewAPI probes the gateway with the credentials in the form (falling back
// to the stored password when the form still shows the redaction placeholder).
func (r *Router) testNewAPI(w http.ResponseWriter, req *http.Request) {
	if r.prober == nil {
		writeErr(w, http.StatusNotImplemented, errors.New("本实例未启用连接测试"))
		return
	}
	var in struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}

	cur := r.cfg.MustLoad()
	// A blank or redacted password means "use the stored one", so testing an
	// unchanged form does not require retyping the secret.
	if in.Password == "" || in.Password == config.RedactedPassword {
		in.Password = cur.NewAPI.Password
	}
	url := strings.TrimSpace(in.URL)
	if url == "" {
		url = cur.NewAPI.URL
	}
	if url == "" || in.Username == "" || in.Password == "" {
		writeErr(w, 400, errors.New("请先填写 new-api 地址、账号与密码"))
		return
	}
	if err := (config.NewAPI{URL: url, Username: in.Username, Password: in.Password}).Validate(); err != nil {
		writeErr(w, 400, err)
		return
	}

	user, err := r.prober.ProbeGateway(url, in.Username, in.Password)
	if err != nil {
		writeJSON(w, 200, map[string]any{
			"results": []checkResult{{
				Name:  "new-api 登录",
				OK:    false,
				Error: err.Error(),
			}},
		})
		return
	}

	detail := fmt.Sprintf("已登录：%s", user.Username)
	if user.DisplayName != "" {
		detail += fmt.Sprintf("（%s）", user.DisplayName)
	}
	detail += fmt.Sprintf("，id=%d，role=%d", user.ID, user.Role)
	if user.Group != "" {
		detail += fmt.Sprintf("，分组 %s", user.Group)
	}
	writeJSON(w, 200, map[string]any{
		"results": []checkResult{{Name: "new-api 登录", OK: true, Detail: detail}},
	})
}

// testNapCat probes each configured NapCat transport independently. They are
// separate hops - the panel, the OneBot HTTP endpoint, the event WebSocket - and
// a failure in one does not mean the others are broken, so each is reported on
// its own. A transport that is simply not configured is marked skipped rather
// than failed.
func (r *Router) testNapCat(w http.ResponseWriter, req *http.Request) {
	if r.prober == nil {
		writeErr(w, http.StatusNotImplemented, errors.New("本实例未启用连接测试"))
		return
	}
	var in struct {
		WebUIURL     string `json:"webui_url"`
		WebUIToken   string `json:"webui_token"`
		OneBotURL    string `json:"onebot_url"`
		OneBotToken  string `json:"onebot_token"`
		EventWSURL   string `json:"event_ws_url"`
		EventWSToken string `json:"event_ws_token"`
	}
	if err := decode(req, &in); err != nil {
		writeErr(w, 400, fmt.Errorf("invalid JSON: %w", err))
		return
	}

	cur := r.cfg.MustLoad()
	n := config.NapCat{
		WebUIURL:     pick(in.WebUIURL, cur.NapCat.WebUIURL),
		WebUIToken:   pickSecret(in.WebUIToken, cur.NapCat.WebUIToken),
		OneBotURL:    pick(in.OneBotURL, cur.NapCat.OneBotURL),
		OneBotToken:  pickSecret(in.OneBotToken, cur.NapCat.OneBotToken),
		EventWSURL:   pick(in.EventWSURL, cur.NapCat.EventWSURL),
		EventWSToken: pickSecret(in.EventWSToken, cur.NapCat.EventWSToken),
	}
	if err := n.Validate(); err != nil {
		writeErr(w, 400, err)
		return
	}

	results := []checkResult{
		r.checkWebUI(n),
		r.checkOneBot(n),
		r.checkEventWS(n),
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

// checkWebUI logs in to NapCat's panel. This is the hop that also proves the
// token is right, since the login exchange hashes it.
func (r *Router) checkWebUI(n config.NapCat) checkResult {
	res := checkResult{Name: "NapCat WebUI"}
	if n.WebUIURL == "" {
		res.Skipped = true
		res.Detail = "未配置"
		return res
	}
	info, err := r.prober.ProbeWebUI(n.WebUIURL, n.WebUIToken)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	res.Detail = describeBot(info)
	if res.Detail == "" {
		res.Detail = "已登录 NapCat 面板（QQ 未登录，无法读取机器人账号）"
	}
	return res
}

// checkOneBot asks the OneBot HTTP endpoint who it is. This is the hop the
// patrols actually use, so it is the most meaningful one to verify.
func (r *Router) checkOneBot(n config.NapCat) checkResult {
	res := checkResult{Name: "OneBot HTTP"}
	if n.OneBotURL == "" {
		res.Skipped = true
		res.Detail = "未配置（巡检会退回 WebUI 桥）"
		return res
	}
	info, err := r.prober.ProbeOneBot(n.OneBotURL, n.OneBotToken)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	res.Detail = describeBot(info)
	return res
}

// checkEventWS opens the event stream. Without it the service still works, only
// with slower reaction (see ADR-0003), so a failure here is informative rather
// than fatal - the UI says so.
func (r *Router) checkEventWS(n config.NapCat) checkResult {
	res := checkResult{Name: "事件 WebSocket"}
	if n.EventWSURL == "" {
		res.Skipped = true
		res.Detail = "未配置（仅靠轮询，功能不受影响）"
		return res
	}
	if err := r.prober.ProbeEventWS(n.EventWSURL, n.EventWSToken); err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	res.Detail = "已连接，事件推送可用"
	return res
}

// describeBot renders a bot identity, or "" when the endpoint did not report one.
func describeBot(info *napcat.LoginInfo) string {
	if info == nil || info.UserID == 0 {
		return ""
	}
	if info.Nickname != "" {
		return fmt.Sprintf("机器人账号 %d（%s）", info.UserID, info.Nickname)
	}
	return fmt.Sprintf("机器人账号 %d", info.UserID)
}

// pick returns v unless it is blank, in which case the stored value is used, so
// an operator can test a partly-filled form.
func pick(v, stored string) string {
	if strings.TrimSpace(v) == "" {
		return stored
	}
	return strings.TrimSpace(v)
}

// pickSecret is pick for secrets: the redaction placeholder also means "use the
// stored value", since the form never carries the real one back.
func pickSecret(v, stored string) string {
	if strings.TrimSpace(v) == "" || v == config.RedactedPassword {
		return stored
	}
	return v
}
