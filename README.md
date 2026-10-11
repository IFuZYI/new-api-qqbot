# QQBot 群监测

把 QQ 群成员身份与 [new-api](https://github.com/Calcium-Ion/new-api) 账号关联起来的监测服务。

它镜像群成员名单与每日签到榜，按成员的 QQ 邮箱（`qq@qq.com`）把每个人绑定到对应的
new-api 账号，按规则把账号从源分组提升到目标分组，并发放每日签到奖励。

**只读群、不在群里发言**，唯一例外是响应成员发来的绑定与签到指令（指令内容由运营在面板里配置）。

---

## 功能

| 功能 | 说明 |
|---|---|
| 群成员同步 | 从 NapCat 读取机器人所在群的成员名单，成员以 QQ 为主键、**永不删除** |
| 自动绑定 | 用 `qq@qq.com` 精确匹配 new-api 账号；0 个 → 未绑定，多个 → 不猜、留待人工 |
| 绑定指令 | 成员在群里发指令（默认 `/绑定`、`<绑定>`、`>>绑定<<`，可自定义、支持正则）即绑定到 `他的QQ@qq.com` 对应账号 |
| 进群自动绑定 | 成员加入监测群时立即按同一规则匹配并绑定 |
| 绑定巡检 | 周期性重试所有未绑定成员，补上「先进群、后注册」的账号；周期可配 |
| 手动换绑 | 面板里从**任一侧**发起：选 QQ 或选账号，两侧都是下拉选择 |
| 分组转换 | 规则驱动：触发方式（绑定成功时 / 巡检周期）+ 源分组 + 目标分组 + 可选条件 |
| 签到记录 | 两条来源写同一行：打卡榜轮询（带排名）与群消息（指令或任意消息，可配）；按 `(qq, 日期)` 幂等，多轮结果是**并集** |
| 签到奖励 | 每天每人一次，额度在 `[下限, 上限]` 内随机；先落库再发放，失败释放重试 |
| 用户目录 | 以 **QQ 与 new-api 账号双键**取并集，回答「谁没进群」；显示今日签到的来源 |
| 连接测试 | 逐通道探测（new-api 登录 / NapCat WebUI / OneBot HTTP / 事件 WS），失败时指明是哪一段 |
| NapCat 自动配置 | 只填 WebUI 地址即可自动开启 OneBot 适配器并回填连接信息（热改，无需重启） |

**每一类自动化都有独立开关**（绑定指令 / 进群绑定 / 绑定巡检 / 消息签到 / 打卡榜巡检 /
群成员巡检），关掉的功能不会产生任何请求。绑定与签到的三条自动路径都只处理**未绑定**的
成员，打开任一开关都不会覆盖手动换绑的结果。

界面为简体中文。

---

## 架构

```
cmd/qqbot/          入口、参数、按需重建的客户端
internal/store/     SQLite 表结构、迁移、查询
internal/service/   业务逻辑，只依赖 Gateway / QQ 两个接口
internal/command/   指令解析与匹配（字面量 + 正则）
internal/newapi/    new-api 管理 API 客户端
internal/napcat/    OneBot HTTP 客户端 + NapCat WebUI 桥
internal/events/    OneBot WebSocket 监听（指令、进群、消息签到）
internal/api/       HTTP 接口、鉴权、静态文件
internal/config/    连接配置（文件 + 数据库双份）
internal/scheduler/ 按路径拆分的独立巡检
web/src/features/   面板按功能拆分的模块
web/                React + Vite + Tailwind 面板
docs/               设计共识、ADR、审计记录
```

**外部依赖只有两个**：new-api 网关（HTTP API）与 NapCat（QQ 侧）。

---

## 快速开始

### 方式一：Docker Compose（推荐）

```bash
git clone git@github.com:IFuZYI/new-api-qqbot.git
cd new-api-qqbot

cp .env.example .env
# 编辑 .env，至少设置 QQBOT_ADMIN_PASSWORD（≥8 位）
# 面板管理密码。留空则首次打开面板时用表单创建。

docker compose up -d --build
```

打开 `http://<主机>:4096/`，登录后在「连接配置」里填写 new-api 与 NapCat。

数据（SQLite 库 + `config.yaml`）存在命名卷 `qqbot-data` 里，容器重建不丢：

```bash
# 备份
docker run --rm -v qqbot-data:/d -v "$PWD":/b alpine tar czf /b/qqbot-data.tgz -C /d .
# 恢复
docker run --rm -v qqbot-data:/d -v "$PWD":/b alpine tar xzf /b/qqbot-data.tgz -C /d
```

### 方式二：直接运行二进制

需要 Go 1.27+；面板是预构建的前端产物，改前端才需要 Node。

```bash
go build -o qqbot ./cmd/qqbot
./qqbot -addr :4096 -data data -web web/dist
```

前端（仅在改动 `web/` 后需要）：

```bash
cd web && npm install && npm run build
```

---

## 配置

### 面板管理账号

首次打开面板会要求设置管理账号（本项目自己的账号，与 new-api 无关）。之后同一表单用于登录。

也可以在 `.env`（**仓库根目录**，与 `.env.example` 同级）里预置，适合无人值守部署：

```bash
QQBOT_ADMIN_USERNAME=admin
QQBOT_ADMIN_PASSWORD=至少8位
QQBOT_PORT=4096
```

服务在**开始接受请求之前**读取它，所以新实例不会有一瞬间对先到者敞开。这是密码丢失时的
恢复路径：改值、重启即重置。**留空 = 不动已有账号**，所以模板里的空行不会清掉密码。

优先级：显式 `-addr` 参数 > `QQBOT_PORT` > 默认 `:4096`。

### 连接配置（面板里填）

| 项 | 说明 |
|---|---|
| new-api URL / 账号 / 密码 | 需要一个**管理员**账号（能改用户分组与额度） |
| NapCat WebUI 地址 / Token | 面板地址，如 `http://127.0.0.1:6099`；填好后可点「自动配置」 |
| OneBot HTTP URL / Token | 巡检调用动作走这里，自动配置会填好 |
| 事件 WebSocket URL / Token | 接收进群与绑定指令；留空也能工作，只是反应慢 |

配置同时写入 `data/config.yaml` 与数据库，两种改法都有效且不会互相覆盖：

- 手改 `config.yaml` → 服务**下一次读取时**采用，**不需要重启**
- 在面板保存 → 同时写入数据库与该文件

> `config.yaml` 里密码与 token 是明文（否则无法回写），权限为 `0600`。

### Docker 下的 NapCat 地址

NapCat 通常跑在宿主机上，容器内的 `127.0.0.1` 指向容器自己。所以容器里填地址要用
`host.docker.internal`：

```
WebUI 地址     http://host.docker.internal:6099
OneBot HTTP    http://host.docker.internal:3002
事件 WebSocket ws://host.docker.internal:3001
```

`docker-compose.yml` 已加 `extra_hosts: host.docker.internal:host-gateway`，Linux 上可用。

**还要改 NapCat 侧的监听地址**，这一步最容易漏：

| 组件 | 需要监听 | 原因 |
|---|---|---|
| NapCat WebUI | `0.0.0.0`（默认已是） | 容器要能连到面板 |
| OneBot 适配器 | **`0.0.0.0`** | 默认只绑 `127.0.0.1`，容器**永远**连不上宿主回环 |

OneBot 适配器可以在「连接配置 → NapCat → 自动配置 → 高级选项 → **适配器监听地址**」里填
`0.0.0.0`，服务会按这个地址改写 NapCat 的适配器配置。**服务返回并保存的连接 URL 仍会是可拨号
的地址（不是 `0.0.0.0`）**，两者是分开的：绑定地址决定"谁能连进来"，URL 决定"往哪连"。

> 暴露适配器前请确认 OneBot token 不为空——绑 `0.0.0.0` 意味着同网络的其他机器也能访问它。

---

## 运维

### 监测设置（面板）

页面按功能分区，每一块都有自己的开关；关掉的功能不会产生任何请求。

| 设置 | 默认 | 说明 |
|---|---|---|
| 绑定指令 | 开 | 指令列表可自定义（一行一个，`/表达式/` 为正则） |
| 进群自动绑定 | 开 | 成员进群时立即按 QQ 邮箱匹配 |
| 绑定巡检 | 开，60 分钟 | 周期性重试未绑定成员；间隔 0 = 暂停定时（保留开关状态） |
| 消息签到 | 开 | 触发方式为「按指令」（默认 `/签到`、`<签到>`、`>>签到<<`）或「任意消息」 |
| 打卡榜巡检 | 开，30 分钟 | 打卡没有事件通知，只能轮询；**关闭后只是不再从打卡榜记录签到**，消息签发的奖励照发 |
| 签到奖励区间 | 100–200 quota | 每天每人在区间内随机；上下限相同即固定 |
| 群成员巡检 | 开，120 分钟 | 兜底；新成员进群与绑定成功会立即记录 |
| 时区 | `Asia/Shanghai` | 决定「今天」的边界 |

指令的匹配规则：**整条消息**与某一行完全一致才生效，前后空格忽略；`/表达式/` 与
`/表达式/i`（忽略大小写）为正则。写错的正则在保存时就会被拒绝（400），不会变成一条永远
不生效的指令。签到指令列表**留空**表示任意消息都算签到。

### 手动触发

面板「概览」页可立即执行：同步成员、自动绑定、分组提升、同步打卡、发放奖励、**手动签到**
（为选定的群成员记录一次消息签到，用于验证「消息即签到」这条路；同一天只记一次）。

### 健康检查

```bash
curl -s http://127.0.0.1:4096/api/auth/state
# {"authenticated":false,"needs_setup":false,"username":""}
```

该接口无需登录，足够判断 HTTP 服务是否存活（容器 HEALTHCHECK 用的就是它）。

`GET /api/status`（需登录）返回每条巡检的上次运行时间与错误，连同设置一起返回：只看时间
无法区分「还没跑」与「已停用」。

---

## 开发

```bash
go build ./...                  # 编译
go test ./internal/...          # 全部测试（离线，两个外部系统都用假实现）
go vet ./... && gofmt -l internal/ cmd/

cd web && npm install && npm run build   # 前端：先 tsc 类型检查再打包
```

测试是纯 Go 且使用假实现，**不需要网络或真实服务**。任何要与真实网关或 NapCat 交互的验证
都必须手动进行，且**不要**放进 `go test`。

### 约定

- 提交信息用 Conventional Commits 带 scope，小写：`feat(service): …`、`fix(store): …`
- 业务逻辑放在 `internal/service`，只依赖 `Gateway` 与 `QQ` 两个小接口
- 界面文案用简体中文

更多设计背景见 `docs/`：

- `docs/design-notes.md` — 已实测确认的事实
- `docs/adr/` — 决策记录（为什么绑定存本地、为什么轮询是唯一真相来源等）
- `docs/implementation.md` — 已实现的形态与验证方式
- `AGENTS.md` — 给自动化协作者的约定与陷阱清单

---

## 已知限制

- **new-api 管理员不能管理同级或更高级账号**（网关规则，不是缺陷）。验证时用普通账号（role 1）。
- **new-api 有活跃会话上限**（默认 50，有效期 30 天）。本服务缓存登录会话，但反复手动登录仍会占满。
- **NapCat WebUI 凭证**默认用进程级密钥签名，NapCat 重启后失效，本服务会重登（有频率限制）。
- **`localhost` 会被规范成 `127.0.0.1`**：OneBot 适配器只监听 IPv4 回环，而 `localhost` 常先解析到
  `::1`，两者不通。
- **打卡榜的日期由 NapCat 决定**（它用 UTC 日期查询），在 UTC 与本地日期不同的时段可能查错日期。
- 签到榜的 `rank` 是 NapCat 换算过的值，只作展示，不是稳定标识。
- **消息签到依赖事件流**（WebSocket）。事件流断开时消息签到不会记录，但打卡榜巡检与奖励
  发放不受影响；「任意消息」模式下若事件流长期不可用，会表现为签到偏少而不是出错。

---

## 许可

见仓库。
