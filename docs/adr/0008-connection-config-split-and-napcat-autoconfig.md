# 0008 连接配置按区块独立保存，NapCat 由 WebUI 地址自动配置

「连接配置」页面从一张卡片拆成两张：**new-api 管理员账号**与 **NapCat**，各自独立保存、
各有自己的 revision。同时新增「自动配置」：只填 NapCat 的 WebUI 地址与 Token，服务登录
NapCat 后**自动改写它的 OneBot 适配器**（开启 `httpServers` + `websocketServers`），并把
得到的 OneBot / 事件地址与 token 回填到页面。

## 为什么拆成两张卡

原来的单卡保存把整个 `Config` 一起提交，服务端只对四个**密钥**字段做回填（`Merge`），
其余字段一律取浏览器里的旧值。于是一个只改了网关地址的表单，会把 NapCat 一半按它打开页面
时的旧值写回去——手改 `config.yaml` 的改动就这样被覆盖（审计 C 的 Finding 5）。而且只要
两半里有一半没填完，另一半也保存不了。

拆开后：

- **区块级保存**：`POST /api/config/newapi` 与 `POST /api/config/napcat` 各自只替换自己那
  一半（`config.Manager.SaveSection`），另一半原样保留。
- **区块级 revision**：`GET /api/config` 返回 `revisions.{newapi,napcat}`，每张卡只在自己
  那半被改动时才提示「已被修改」。用整份配置的哈希会让「保存网关」把打开的 NapCat 卡判成
  过期，反之亦然——那是误报。
- 保存一半不再依赖另一半是否合法，半填的表单不会互相阻塞。

## 为什么「只填 WebUI 地址」能自动配置

NapCat 的 OneBot 配置可以**热改**（`/api/OB11Config/SetConfig` → `save` + `reloadNetwork`，
见 ADR-0001），所以服务能替操作者把适配器配好，无需手改文件、无需重启：

- 登录 WebUI 用 `sha256(webui_token + ".napcat")` 换 1 小时凭证（沿用既有实现）。
- 读出当前 OneBot 配置，**就地替换**本项目自己的两个适配器（名字固定
  `qqbot-http` / `qqbot-ws`），其它适配器原样保留，因此重复执行是**幂等**的，也不会碰操作者
  自己配的适配器。
- token 复用：优先用请求里给的，其次用这两个适配器上已有的，最后才新生成——重复执行不会
  把正在跑的客户端的 token 换掉。
- 适配器**绑定**在 `127.0.0.1`（不外露），而回填的 URL 用**操作者填的 WebUI 主机**（例如
  `localhost`），因为那是本服务去连它的地址。**但 `localhost` 会被改写成 `127.0.0.1`**：
  适配器只监听 IPv4 回环，而 `localhost` 通常先解析到 `::1`，`::1` 连不上绑定在
  `127.0.0.1` 的监听器——于是 URL 明明「看着对」却 `connection refused`（实测踩到）。

## WebUI 地址要先归一化到源

操作者粘贴的是浏览器里的地址，也就是面板所在的 `.../webui/`，但 NapCat 的 API 在**根路径**
`/api/...`。因此 `NewWebUI` 会先把地址归一化到 `scheme://host[:port]`（`NormalizeWebUIURL`）。
否则每次请求都会打到 `.../webui/api/...` 并拿回 SPA 的 HTML，而不是 JSON——这正是旧配置里
`webui_url: http://localhost:6099/webui/` 会导致 WebUI 调用全部失败的原因。

## 边界与约定

- **自动配置需要 NapCat 已登录 QQ**：`OB11Config` 接口在未登录时返回 `Not Login`，此时
  报错并保持原配置不变（`EnsureAdapters` 只在写成功后才回填）。
- **只碰自己的适配器**：`upsertAdapter` 按名字替换/追加，其它 `websocketClients` /
  `httpClients` / `plugins` 等一律不动。
- **端口**默认 `3001`（WS）/ `3002`（HTTP），与部署笔记一致；可在「高级选项」里改。
- **回填是一次区块保存**：自动配置成功后，把 WebUI 地址/Token 与 OneBot/事件地址/Token
  一起写进 NapCat 区块，并同样受该区块 revision 保护。
- **未启用自动配置**（路由未注入 `NapCatAdmin`）时，接口返回 `501`，不假装成功。
- **WebUI 登录按窗口限额**：NapCat 只允许每 60 秒 10 次登录，而凭证随 NapCat 重启即失效
  （签名密钥是每进程的，除非设了 `NAPCAT_WEBUI_JWT_SECRET_KEY`）。凭证被拒时缓存会被丢弃，
  下一次调用就要重新登录；若不加限制，一次覆盖 200 个群的巡检会花掉 200 次登录，把额度用尽，
  回退路径随即以 `login rate limit` 失败——恰好在它最该生效的场景里失效（实测：20 次被拒的读取
  → 20 次登录）。因此登录尝试按窗口记账（默认 60 秒 3 次）：既封住巡检的消耗，又保留
  「被拒后重新登录」这条恢复路径。地址或 token 变了会重置窗口，因为那正是操作者的修复手段。
