# QQBot 群监测 — 设计共识

把 QQ 群成员身份与 new-api（`https://api.fuzy.site`，系统名 FuZY）账号关联起来的监测服务。
只读群、不发消息（唯一例外：响应成员发来的绑定指令）。

## 已实测确认的事实

| 事实 | 证据 |
|---|---|
| 目标实例 `https://api.fuzy.site`，`v1.0.0-rc.43`，`quota_per_unit=500000` | `GET /api/status` |
| 管理员账号 `napcat` 可登录，`id=3`，**`role=10`（Admin）** | `POST /api/user/login` + `/api/user/self` |
| 分组现为 `authorized` / `default` / `vip`（`test` 已消失） | `GET /api/user/groups` |
| **Admin 足以完成全部写操作**：改组 `default↔authorized`、加/减额度均成功 | `PUT /api/user/`、`POST /api/user/manage`（在 `test` 用户上实测并已回滚） |
| `role=10` **不能**读 `/api/option/`（403）—— 仅排障受限，不影响功能 | `GET /api/option/` → 403 |
| 注册已关、Turnstile 关、密码登录开、new-api 自己的签到关 | `GET /api/status` |
| 机器人 `3688665216`（fuzy）在线，在群 `1129535731`（3 人） | OneBot `get_group_list` / `get_login_info` |
| NapCat WebUI 有直连 OneBot 桥，且 OneBot 配置可**热改**（无需重启） | `POST /api/Debug/call`；`/api/OB11Config/SetConfig` → `save`+`reloadNetwork` |
| NapCat **没有签到事件**，打卡只能轮询 `get_group_signed_list` | 事件目录无签到类事件 |

## 权限决定：只用 Admin，不提 root

实测 Admin（`role=10`）能改组、能发额度，两者都只要求「操作者角色高于目标角色」，
而目标是普通用户（`role=1`）。因此**不把 `napcat` 提为 root**，避免越权面。代价：读不到
`/api/option/`（需 root），排障时只能靠公开的 `/api/user/groups`。

## 写操作的正确姿势

- **改组**：`PUT /api/user/`，body 必须是**完整用户对象**（`id`+`username`+`display_name`
  +`role`+`group`+`remark`），new-api 只取白名单字段（`username`/`display_name`/`group`/
  `remark`，`password` 非空才改）。只改组、不动密码与权限矩阵时**不触发 step-up 验证**。
- **发额度**：`POST /api/user/manage` `{"id":N,"action":"add_quota","mode":"add","value":N}`。
  new-api 自己记账，本库只留「今日已发」守卫，不镜像流水。

## 奖励规则

每天签到发放 **`[min,max]` 区间内随机**的额度（与 new-api 自己签到的口径一致）。
测试期 `100–200`。随机值每天每用户只取一次，取到即发、发完落库，重跑不得再取新值。
见 ADR-0004。

## 绑定

绑定关系存本库（`qq → newapi_user_id`），new-api 只读（见 ADR-0002）。三条路径：

1. **自动匹配**：用 `qq@qq.com` 调 `GET /api/user/search?keyword=`，命中且**精确等于**
   `qq@qq.com` 才绑定；0 个 → 未绑定；多个 → 不自动绑，交管理员。
2. **自动刷新**：未绑定的成员按周期重试匹配（账号可能后建）。
3. **绑定指令**：成员在群里发 `>>绑定<<`，把其 QQ 号绑到 `qq@qq.com` 对应的账号；
   不允许指定别的邮箱。账号不存在则拒绝。

管理员可在 WebUI 手动换绑到任意 new-api 账号。

## 巡检

轮询是唯一真相来源，各自独立周期（见 ADR-0003）：用户列表 2h、打卡榜单 30min，都可配。
打卡按 `(qq, 日期)` 做**并集**去重。OneBot 事件只用于加速（如 `group_increase`），
不参与正确性论证。

## 本机 NapCat 已完成的配置

通过 WebUI 热写入 `onebot11_3688665216.json`（无需重启，实测生效）：

| 适配器 | 地址 | 用途 |
|---|---|---|
| `websocketServers`「qqbot-ws」 | `127.0.0.1:3001` | 事件推送 + 收发动作（绑定指令走这条） |
| `httpServers`「qqbot-http」 | `127.0.0.1:3002` | 主动调动作（巡检用，短连接） |

共用同一 token（32 位 hex，暂存 `/root/.hermes/cache/scratch/napcat_ob11_token.txt`，
正式实现时移入本项目配置/数据库）。原先两者都是空数组。

## 已落盘文档

- `GLOSSARY.md` — 领域术语
- `docs/adr/0001-napcat-webui-onebot-bridge.md` — 走 WebUI 的 OneBot 桥
- `docs/adr/0002-binding-owned-locally.md` — 绑定存本库，new-api 只读
- `docs/adr/0003-polling-authoritative-events-optional.md` — 巡检是唯一真相来源
- `docs/adr/0004-reward-random-range.md` — 奖励区间随机
