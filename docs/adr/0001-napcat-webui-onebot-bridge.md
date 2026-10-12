# 0001 通过 NapCat WebUI 的 OneBot 桥接入 QQ

NapCat 的 WebUI（`http://127.0.0.1:6099`）提供 `POST /api/Debug/call`，可以把任意 OneBot
动作直接投给运行中的适配器：`{"action":"get_group_list","params":{}}` → 返回标准 OneBot
响应体。用它就不必修改 NapCat 的 `onebot11_*.json` 去开 httpServers/websocketServers
（那需要改配置文件并重启 NapCat，会短暂掉线）。

代价：鉴权要先 `POST /api/auth/login`，提交 `sha256(webui_token + ".napcat")`，换回一个
1 小时有效的 Credential；且凭证是 HMAC 签名的，密钥 `NAPCAT_WEBUI_JWT_SECRET_KEY` 未设置时
是进程启动时的随机值 —— 所以凭证在 NapCat 重启后一律失效，必须重登（登录接口有频率限制，
需要退避重试）。

替代方案（未采用）：把本项目做成 OneBot ws 客户端连 NapCat 的 websocketServers，或让
NapCat 连本项目的 ws 服务端。那条路能拿到实时事件推送（新成员进群），但要改 NapCat 配置
并重启；本方案靠周期巡检补足实时性。

## 修订（2026-10-12，ADR-0008 之后）

上面「替代方案（未采用）」里的取舍**已不再成立**：NapCat 的 OneBot 配置可以经 WebUI
热写（`/api/OB11Config/SetConfig` → save + reload，无需重启），本项目现在也会主动启用
`httpServers` / `websocketServers`（「连接配置 → 自动配置」，见 ADR-0008）。因此
WebSocket 事件推送已经是**采用**的方案，只用于加速（见 ADR-0003 的修订）：成员名册与
打卡榜仍以轮询为准，但进群、绑定指令与消息签到依赖事件流。

WebUI 桥（`/api/Debug/call`）保留为**回退路径**：OneBot 适配器不可用（例如 QQ 登录态
失效）时它仍能读数据，因为 NapCat 在登录前就启动 WebUI。凭证在 NapCat 重启后失效这一条
依然正确，所以客户端必须能在被拒绝时重新登录，而不是把死凭证缓存到底。
