# QQBot 群监测 — 实现说明

把 QQ 群成员身份与 new-api（`https://api.fuzy.site`，系统名 FuZY）账号关联起来的监测服务。
只读群、不发消息（唯一例外：响应成员发来的 `>>绑定<<`）。设计共识见 `docs/design-notes.md`
与 `docs/adr/`；本文件记录**已实现**的形态与验证结果。

## 运行

```bash
cd /home/QQbot
go build -o qqbot ./cmd/qqbot
./qqbot -addr :4096 -data data -web web/dist
```

前端（改了 `web/` 才需要）：

```bash
cd web && npm install && npm run build
```

首次打开 `http://<host>:4096/` 会要求设置管理账号（本项目自己的账号，与 new-api 无关）。
之后同一表单用于登录。

## 结构

| 包 | 职责 |
|---|---|
| `cmd/qqbot` | 入口、参数、按需重建的网关/NapCat 客户端 |
| `internal/store` | SQLite 表结构、迁移、查询 |
| `internal/service` | 业务逻辑（同步/绑定/提升/发奖），只依赖 `Gateway`、`QQ` 两个接口 |
| `internal/newapi` | new-api 管理接口客户端 |
| `internal/napcat` | OneBot HTTP 客户端 + NapCat WebUI 桥 |
| `internal/events` | OneBot WebSocket 监听（绑定指令） |
| `internal/api` | HTTP 接口、鉴权、静态文件 |
| `internal/config` | 连接配置（存数据库） |
| `internal/scheduler` | 两条独立周期的巡检 |

## 已实现的行为

- **群管理**：从 QQ 读取机器人账号所在的群，勾选一个或多个监测；新发现的群默认开启监测。
- **分组转换（规则）**：管理员在「分组转换」菜单里配置规则 —— 触发方式（绑定成功时 / 巡检
  周期分钟数 / 两者）、源分组（可多选，从 new-api 拉取，留空 = 任意）、目标分组（从 new-api
  拉取或手填）、以及可选条件（群等级、QQ 等级、入群天数、累计签到天数、new-api 余额、
  new-api 请求数，条件之间是「且」）。规则在**绑定成功时**（自动匹配、`>>绑定<<` 指令、
  管理员手动绑定三条路径都会触发）和**各自的巡检周期**执行。既无触发方式又无条件的规则
  视为未完成，不匹配任何人。
- **成员同步**：成员以 QQ 为主键；群关系存在 `member_groups`，成员退出某个群不会删成员行，
  绑定与签到历史得以保留。
- **自动绑定**：用 `qq@qq.com` 查 new-api，**精确且唯一**才绑定；0 个 → 未绑定；多个 → 不绑定
  并计数。未绑定的成员每次自动绑定都会重试（账号可能后建）。
- **绑定指令**：成员在监测群发 `>>绑定<<` 即绑定到 `他的QQ@qq.com` 对应账号；不接受指定别的邮箱。
  经 OneBot WebSocket 事件触发；事件通道不可用不影响其它功能。
- **手动换绑**：WebUI 用户管理里可把任意 QQ 改指到任意 new-api 账号，也可解绑。
- **分组提升**：只把处于**源分组**的账号提升到**目标分组**；未绑定、已在其它分组的都跳过；
  单个失败不中断整轮；重复执行是空操作。写入前按 id 重读账号，避免 new-api 的
  `PUT /api/user/` 覆盖式更新把 `display_name` 清空。
- **签到**：轮询 `get_group_signed_list`，按 `(qq, 日期)` 幂等入库（榜单只含当日且分页，
  多轮结果是**并集**）；累计天数即 `checkins` 的行数。
- **发奖**：每天每人一次，额度在 `[下限, 上限]` 内随机；发放前先把金额落库，用
  `rewarded_at` 占位，网关失败才释放重试，成功则永不重发。
- **巡检**：成员列表与打卡榜各自独立周期（默认 120 / 30 分钟，可配），每次循环重新读取配置，
  改设置无需重启。启动后延迟 20 秒再跑首轮。
- **鉴权**：除 `/api/auth/{state,setup,login,logout}` 外全部接口需要登录会话。

## 验证方式

- `go test ./internal/...` —— 全离线，两个外部系统都用假实现。
- 真实联调（手动，不放进 `go test`）：
  - `POST /api/groups/refresh` 读到真实群 `1129535731`；
  - `POST /api/actions/sync` → 3 名成员；
  - `POST /api/actions/bind` → 全部 pending（群里三人未在 FuZY 注册对应邮箱）；
  - `POST /api/actions/promote` 把测试账号从 `default` 提到 `authorized`，`display_name` 保留，
    已还原；
  - `POST /api/actions/award` 真发额度 108（在 100–200 区间内），重复执行不重发，已回滚；
  - 浏览器实测首次设置、登录、五个页面渲染、`>>绑定<<` 经真实 WebSocket 触发。
- 已知边界：**管理员账号不能管理同级或更高级账号**。用 `napcat`（role 10）去改组/发额度到
  它自己（也是 role 10）会被拒 —— 这是 new-api 的规则，不是缺陷。验证时请用普通账号（role 1）。

## 尚未实现 / 需注意

- 打卡榜的 `rank` 是 NapCat 换算过的值（`(signInRank-1)/2+1`），只作展示，不要当稳定标识。
- 事件监听目前只处理 `>>绑定<<`；`group_increase` 未接（新成员靠成员列表巡检发现，
  最长等一个成员巡检周期）。
- NapCat WebUI 凭证在 NapCat 重启后失效（除非设了 `NAPCAT_WEBUI_JWT_SECRET_KEY`），
  本服务会重登，注意登录频率限制。
