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
单位是 new-api 自己的 **quota**：走 `POST /api/user/manage` 的 `add_quota`，直接加到账号余额；
quota 与货币的换算由实例的「额度显示」设置决定（默认 500000 quota = $1），本系统只按 quota
收发、不做换算。测试期 `100–200`。随机值每天每用户只取一次，取到即发、发完落库，重跑不得再取新值。
见 ADR-0004。

## 绑定

绑定关系存本库（`qq → newapi_user_id`），new-api 只读（见 ADR-0002）。四条路径，
**每条都在设置里独立开关**，且都只处理未绑定的成员（见 ADR-0009）：

1. **自动匹配（巡检）**：用 `qq@qq.com` 调 `GET /api/user/search?keyword=`，命中且**精确等于**
   `qq@qq.com` 才绑定；0 个 → 未绑定；多个 → 不自动绑，交管理员。按自己的周期重试
   （默认 60 分钟），用于补上「先进群、后注册」的账号。
2. **进群自动绑定**：`group_increase` 事件到达时立即按同一规则匹配。
3. **绑定指令**：成员在群里发指令（默认 `/绑定`、`<绑定>`、`>>绑定<<`，可在设置里改成
   任意多条、支持正则），把其 QQ 号绑到 `qq@qq.com` 对应的账号；不允许指定别的邮箱。
   账号不存在则拒绝。
4. **管理员手动换绑**：WebUI 里可绑到任意 new-api 账号。

前三条共用 `Service.bindQQ`，遇到已有绑定一律跳过：打开任一开关都不会覆盖手工换绑的结果。

## 签到

签到的身份是 `(QQ, 日期)`，**来源只是属性**（见 ADR-0009）：

- **打卡榜巡检**（默认 30 分钟）：读 QQ 群自带打卡榜，写入 `source=board`，带排名与打卡时间。
- **消息签到**：成员发消息即算当天签到。指令列表非空时只认列出的指令；**留空表示任意消息**。
  写入 `source=message`，重复消息不会重复记录。

两条来源写同一行：打卡榜后到会**升级**消息来源那行（它知道得更多），消息后到则不覆盖任何
字段。两者都不动 `reward_quota`/`rewarded_at`，所以「每天每人一次奖励」只由奖励领取的原子
更新保证。每种自动化都有独立开关，关掉的功能不会产生任何请求。

**签到以绑定为前提**：奖励发到成员绑定的 new-api 账号上，所以没有绑定的成员签到无法发放，
两条来源都不记录（消息签到返回 `unbound`，打卡榜只把这个人记成成员）。这不是错误——绑定
巡检会把他绑上，下一轮轮询就把当天那行补上；「先跳过、绑定后补记」有测试钉住。奖励发放同样
跳过无绑定的行（旧版本可能留下的数据）。

## 巡检

轮询是唯一真相来源，各自独立周期（见 ADR-0003）：成员列表 2h、绑定 60min、打卡榜单 30min，
都可配、都可单独关闭。打卡按 `(qq, 日期)` 做**并集**去重。OneBot 事件只用于加速
（`group_increase`、指令消息、消息签到），不参与正确性论证。`GET /api/status` 返回每条
巡检的上次运行时间与错误，连同开关一起看才能区分「还没跑」和「已停用」。

### 手动执行走同一条路

面板上每条巡检都有**测试一次**（`POST /api/patrols/{name}/run` → `scheduler.RunNow`）。它跑
的就是定时器跑的那个巡检体，并且经过同一处记录，所以手动与自动不会出现两种行为：测试报成功
而定时器会失败（或相反）比没有测试更糟。记录下来的结果同时让行上的徽标与「上次运行」立刻更新，
否则运营点完按钮看到行里还写着「尚未运行」，无法判断到底跑没跑。

**关掉开关的巡检同样可以手动测试**，这是有意的：「关掉的功能不产生请求」约束的是自动循环，
点「测试一次」是一次明确的请求——运营正是靠它决定要不要打开这个功能。手动执行不改变定时器
状态（测试一个关掉的巡检，循环仍然闲置）。

### 关掉的功能不产生请求

「模块化」不只是把代码拆开，还要让关掉的功能**真的不再调 NapCat**（见 ADR-0009）：

| 关掉的东西 | 省下的调用 |
|---|---|
| 成员列表巡检 | 每周期每组一次 `get_group_member_list` |
| 打卡榜巡检 | 每周期每组一次 `get_group_signed_list`（真实签名请求，最贵的一个） |
| 绑定巡检 | 未绑定成员每周期各一次账号查询（走 new-api，不碰 QQ） |
| 群成员巡检关掉后，进群事件仍会记录成员 | —— |
| 规则里没有 QQ 侧条件（群等级/QQ 等级/入群天数） | 该批规则不读任何群成员列表 |

最后一条由 `Rule.NeedsQQData` 判定：绑定路径上它最值钱——`AutoBind` 每绑一个人就会跑一次
绑定规则，若规则只看 new-api 余额，就不该为此读遍所有监测群的成员列表。

**一次巡检里，每个监测群只读一次成员列表，不按人数重复读。** `qqDataCache` 的生命周期是
一次巡检，沿 `bindQQ` → `ApplyOnBindRules` → `memberContext` 传下去：一次绑定 6 个人、3 个
群，就是 3 次请求（实测），而不是 18 次。缓存在 `contextFor` 里按需加载，所以调用方忘了预热
也只是多读一次，不会退化成「查不到任何人的 QQ 数据」——那种情况下所有 QQ 侧条件都会
fail closed，表现成「规则好像不生效」，是最难查的一种。

**同一个人在多群里时，各条路径的取值必须一致。** 绑定触发与巡检都走 `mergeContext`，
每个字段取最大值（群等级 / QQ 等级 / 入群天数）。只合并其中一部分会让结果取决于群的读取
顺序，于是同一条规则可能绑定时就命中、下次巡检又不命中。

**奖励由签到循环发放**，所以该循环在「打卡榜巡检」或「消息签到」任一开启时都会运行；
关掉打卡榜只跳过读榜，不会停止发奖（否则只开消息签到的人永远拿不到额度）。

**服务整个生命周期只登录一次。** 登录会新建一个会话，而 new-api 从不按设备去重（上游
`createLoginSession` 每次登录都新发一个 SID，User-Agent 只被记录在会话行上），账号的活跃会话
上限（默认 50）就是被重复登录用满的。所以除了「拿到第一条会话」，其余一切都走
`POST /api/user/auth/refresh`：访问令牌只活 15 分钟，会话活 30 天，刷新用登录下发的
`new_api_refresh` cookie 换新令牌并**轮换刷新密钥**，轮换后的值会立刻回写存储（否则重启会用
旧密钥，被网关判为 reuse 而作废该会话，反而逼出一次登录）。已实测：冷启动 1 次登录，之后跨令牌
过期与多次重启都是 0 次登录。

**没有会话时不去试刷新。** 刷新接口与登录同在 `CriticalRateLimit` 之下，一次注定失败的刷新会
白白花掉同一个 20 次/20 分钟的额度，所以 `Reauthenticate` 先看有没有会话 cookie，没有就直接登录。
由 `internal/newapi` 的 `TestReauthenticateLogsInDirectlyWithoutASession` 钉住。

**请求带一个稳定的设备指纹**（`newapi.fingerprint`，由面板签名密钥派生，随 `User-Agent` 发出）。
上游把 `User-Agent` 记在会话行上，所以指纹让本服务在会话列表里可辨认（运营能挑出该撤销的那一条），
并让每次重启在网关看来都是同一台设备。它不采集宿主机信息，也不替代上面的会话复用——网关没有
「按设备复用会话」的接口，能阻止重复登录的只有复用那条会话。

**刷新请求必须带同源 `Origin`。** new-api 在刷新路由上装了 `SessionCookieOriginGuard`：当
`SESSION_COOKIE_SECURE=true`（它自己的启动警告就让生产环境这么设）时，缺 `Origin`/`Referer`
的请求一律 403。登录路由**没有**这道守卫，而刷新有——所以不带 Origin 的客户端在那种实例上
**刷新完全不可用**：已实测，每次刷新都被 403 `AUTH_ORIGIN_FORBIDDEN` 拒绝、每次调用都失败
（403 不被当作会话已死，所以不会退化成「每次重新登录」，是直接不可用）。客户端因此发送与网关
同源的 `Origin`（即配置里的网关地址），并把 `AUTH_ORIGIN_FORBIDDEN` 明确列为不可重登的一类——
它在 401 上出现时尤其要紧，否则裸 401 规则会把它当会话已死而触发登录。由
`TestRefreshSendsASameOriginHeader` 与 `TestOriginRefusalIsNotADeadSession`（403 与 401 两种形态）钉住。

> 守卫**不信任** `X-Forwarded-Proto`（它看的是本进程是否真的终止了 TLS，见上游
> `auth_origin_test.go` 的 `...DoesNotTrustForwardedProtoFromClient`）。所以网关若在
> **终止 TLS 的反向代理**后面，new-api 看到的 scheme 是 `http`，运营必须把对外地址填进
> `SESSION_COOKIE_TRUSTED_URL`——否则连它自己的网页面板也过不了这道守卫。本服务发的 Origin
> 就是配置里的网关地址，与该可信地址一致。

**落库的网关会话绑定「网关地址 + 账号 + 密码」，不跨主机复用。** 刷新令牌是**签发它的那台网关**给
这个账号的凭据：把服务指向另一台实例时若照旧导入，就会把 A 实例的凭据原样发给 B（跨主机
凭据泄漏，且服务会继续以旧账号身份行事）；只改账号或只改密码时若照旧导入，服务会继续以
**旧凭据**的身份行事——新密码是错的也看不出来，直到旧会话过期才在不相干的时刻登录失败。
`LoadNewAPISession(origin)` 只在 origin（地址去尾斜杠 + 账号 + 密码）一致时返回令牌；旧版本
留下的裸令牌没有记录 origin，读作「没有会话」，代价是升级后重登一次。
这条由 `cmd/qqbot` 的 `TestStoredSessionIsNotSentToANewHost` 钉住（变异验证：去掉 origin
比较，B 实例立即收到 A 的令牌，测试报「credential leaked across hosts」），以及
`internal/config` 的 `TestSessionIsBoundToItsOrigin`（覆盖改账号 / 改密码 / 尾斜杠）。

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
- `docs/adr/0005-rules-driven-conversion.md` — 分组转换由规则驱动
- `docs/adr/0006-admin-credential-from-env.md` — 管理账号与监听端口可由环境变量预置
- `docs/adr/0007-user-directory-both-keys.md` — 用户目录以 QQ 与 new-api 账号双键取并集
- `docs/adr/0008-connection-config-split-and-napcat-autoconfig.md` — 连接配置分区块保存，NapCat 按 WebUI 地址自动配置
- `docs/adr/0009-bind-and-checkin-paths-are-modular.md` — 绑定与签到按来源拆分，每条路径独立开关
