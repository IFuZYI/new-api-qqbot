# Audit C — config, NapCat/new-api clients, React frontend

Scope: `internal/config/config.go`, `internal/napcat/client.go` + `flexint.go`,
`internal/newapi/client.go`, and `web/src/*` (`api.ts`, `App.tsx`, `Rules.tsx`,
`Login.tsx`, `theme.tsx`, `ui.tsx`). Correctness only; style/naming/docs are out
of scope. Read-only on the repo — Go probes live in a scratch copy under
`/tmp/audit` (never under `internal/`), and the browser probes ran against a
static stub server serving `web/dist`.

Baseline at audit time: `go test ./internal/...` green; `cd web && npm run build`
(`tsc -b --noEmit && vite build`) green, so the TypeScript typechecks and every
frontend finding below is a runtime/state bug, not a type error.

Reproduction commands are given per finding.

---

## Finding 1 — CONFIRMED — A sign-in board with any even-ranked signer fails to decode as a whole, so no check-ins are recorded

**File:** `internal/napcat/client.go:59` (`Rank int`), `client.go:164-171`
(`GetGroupSignedList`), `client.go:123-141` (`decodeAction`).

**What is wrong.** NapCat's `get_group_signed_list` computes the rank as
`(signInRank - 1) / 2 + 1`
(`reference/NapCatQQ/packages/napcat-onebot/action/extends/GetGroupSignedList.ts:45`).
That is JavaScript, where the division is floating-point: for an even
`signInRank` it produces a **fractional** number (`signInRank = 2` → `1.5`,
`4` → `2.5`, …). `SignedMember.Rank` is a plain `int`, and `decodeAction`
unmarshals the whole array in one `json.Unmarshal`. The first fractional rank
therefore fails the entire decode, `GetGroupSignedList` returns an error, and
`SyncCheckins` records **nothing** for that group — the whole board, including
the well-formed integer ranks, is lost.

**Reproduction** (scratch copy):
`/tmp/audit/internal/napcat/napcat_probe_test.go::TestAuditSignedRankFractionalFailsDecode`.

```bash
cd /tmp/audit && /usr/local/go/bin/go test ./internal/napcat/ -run TestAuditSignedRankFractionalFailsDecode -v
# --- FAIL expected; logged:
# decode error: get_group_signed_list: decode data: json: cannot unmarshal number 1.5 into Go struct field .1.rank of type int
```

An all-integer board decodes fine (`TestAuditSignedRankIntegerOK` passes), so the
failure is specifically the fractional rank, not the envelope.

**Impact.** The 2nd signer in any board has `signInRank = 2` → `rank = 1.5`, so
essentially every real board with two or more signers is unreadable. Check-ins
are silently never recorded, so `checkin_days` stays 0 and **no reward is ever
paid** (rewards are computed from recorded check-ins). The failure is doubly
silent because the scheduler erases the board error from its status (see the
sibling report, Audit B Finding 7): the dashboard shows the check-in patrol as
healthy while it has recorded nothing.

---

## Finding 2 — CONFIRMED — `newapi.Groups()` cannot decode the gateway's group list, breaking the rule group pickers

**File:** `internal/newapi/client.go:233-246` (the anonymous `Ratio float64`
field at `client.go:236`), consumed by `internal/service/rules_run.go:13-16`
and served at `internal/api/rules.go:88-94`.

**What is wrong.** `Groups()` decodes `/api/user/groups` into
`map[string]struct{ Desc string; Ratio float64 }`. new-api's `GetUserGroups`
adds an `"auto"` entry whose ratio is the **string** `"auto"`, not a number
(`reference/new-api/controller/group.go:41-46`):

```go
usableGroups["auto"] = map[string]any{"ratio": "auto", "desc": ...}
```

`json.Unmarshal` cannot put `"auto"` into a `float64`, so the whole response
fails and `Groups()` returns an error. The `auto` entry is emitted whenever
`"auto"` is in the instance's usable groups — which is the new-api default, so
this is the normal case.

**Reproduction:** `/tmp/audit/internal/newapi/newapi_probe_test.go::TestAuditGroupsAutoRatioStringFails`.

```bash
cd /tmp/audit && /usr/local/go/bin/go test ./internal/newapi/ -run TestAuditGroups -v
# Groups() error: GET /api/user/groups: decode data: json: cannot unmarshal string into Go struct field .auto.ratio of type float64
```

A numeric-only response decodes fine (`TestAuditGroupsNumericRatioOK`), so it is
the string ratio, not the envelope.

**Impact.** `GET /api/newapi/groups` returns 502, so `Rules.tsx`'s
`api.newAPIGroups()` rejects, its `.catch(() => setGroups([]))`
(`Rules.tsx:32-35`) swallows the error, and both the source-group checkboxes and
the target-group `<select>` come up empty — the operator sees the "未读取到
new-api 分组" hint even though the gateway is reachable and the credentials are
correct. Rule authoring is reduced to hand-typing group names. (No server-side
impact: `ApplyRules` compares `u.Group == rule.TargetGroup` as strings and never
calls `Groups()`, so conversion still works if names are typed correctly.)

---

## Finding 3 — CONFIRMED — `FindUserByEmail` only reads the first page of the search, so a real match is reported as "not found"

**File:** `internal/newapi/client.go:163-191`, specifically the request at
`client.go:173` (`/api/user/search?keyword=…` — no `p` / `page_size`).

**What is wrong.** new-api's `/api/user/search` is paginated and LIKE-matches
`username OR email OR display_name` (and, when the keyword parses as an int,
`id`). The client sends only `keyword` and decodes a single `PageInfo`, so it
only ever sees the server's **first page** — new-api's default page size is 10
(`reference/new-api/common/constants.go:64`, `ItemsPerPage = 10`, capped at 100).
If more than ten rows LIKE-match the keyword, the exact-match row can sit on
page 2+ and the client never requests it, so it reports "no account" and the
member stays `pending` forever. The exact-match narrowing is applied only to
whatever rows the first page happened to contain.

**Reproduction:** `/tmp/audit/internal/newapi/newapi_probe_test.go::TestAuditFindUserByEmailOnlySeesFirstPage`.

```bash
cd /tmp/audit && /usr/local/go/bin/go test ./internal/newapi/ -run TestAuditFindUserByEmail -v
# query sent: "keyword=123456@qq.com" (no p/page_size)
# BUG: the exact match on page 2 is invisible; the client returns not-found
```

`TestAuditFindUserByEmailExactOnFirstPage` confirms a match on page 1 is found,
so the defect is purely the missing pagination.

**Impact.** Auto-binding silently skips a member whose `qq@qq.com` account
exists but whose search row is not on page 1 (a common case once an instance
has many accounts whose emails share digits with the QQ number, or when the
keyword also matches IDs/usernames). The member is retried on every auto-bind
pass and never binds, so they are never promoted and never paid — with no error
surfaced to the operator. This directly contradicts the "exact, unique match"
invariant, which assumes the search returns the candidate set.

---

## Finding 4 — CONFIRMED — An empty or comment-only config file silently wipes the stored configuration

**File:** `internal/config/config.go:113-142` (`syncFromFile`), `98-103`
(`Load`), `169-178` (`EnsureFile`).

**What is wrong.** `syncFromFile` treats "file parses without a YAML error" as
"the operator's new configuration". An empty file (or one containing only the
header comments) parses cleanly into the zero `Config`, so `saveToStore` writes
the empty value over the database and `Load` returns it with **no error**. The
documented guarantee — "A malformed one is reported and the database copy is
returned unchanged, so a typo cannot wipe a working configuration"
(`config.go:96-97`) — only covers files that *fail to parse*; a file that parses
to nothing is adopted as a deliberate "everything empty".

**Reproduction:** `/tmp/audit/internal/config/config_probe_test.go::TestAuditEmptyFileWipesStoredConfig` and `::TestAuditCommentOnlyFileWipesStoredConfig`.

```bash
cd /tmp/audit && /usr/local/go/bin/go test ./internal/config/ -run 'TestAuditEmpty|TestAuditCommentOnly' -v
# after emptying the file: url="" user="" pw=""
# database copy: url="" user=""
# BUG: emptying the file silently wiped the stored config (url now empty, no error)
# BUG: a comment-only file wiped the stored config (no error)
```

`TestAuditMalformedFileProtected` (the existing behaviour) passes, so the gap is
specifically the empty/blank case.

**Impact.** Any way the file ends up empty — an editor that truncates before
writing, a crash between truncate and write, `EnsureFile`-adjacent tooling, a
`> config.yaml` redirection, or an operator who opens it and saves a blank
buffer — destroys the live new-api and NapCat connection settings with no
warning. The next `MustLoad()` (used by `lazyGateway`/`lazyQQ` on every call,
`cmd/qqbot/main.go:112-113,211-224`) returns an empty config, so the service
reports "not configured" and every sweep fails. Recovery requires re-entering
everything in the WebUI. (Note the same shape from the other direction:
`EnsureFile` on an empty database writes an all-empty file — benign there, but
it shows the zero `Config` round-trips through the file as "valid".)

---

## Finding 5 — CONFIRMED — A stale WebUI save overwrites a hand edit to any field the form did not touch

**File:** `internal/api/api.go:121-142` (`saveConfig`), specifically
`in = in.Merge(r.cfg.MustLoad())` at `api.go:127`, plus
`internal/config/config.go:312-327` (`Merge`).

**What is wrong.** The Connection page loads the config once and holds it in
React state. On save the handler takes the **whole** posted object and only
restores the four *secret* fields from the live copy (`Merge` special-cases
`Password`, `WebUIToken`, `OneBotToken`, `EventWSToken`). Every non-secret field
(`newapi.url`, `newapi.username`, `napcat.webui_url`, `napcat.onebot_url`,
`napcat.event_ws_url`) is taken verbatim from the browser's stale copy. So if
the operator hand-edits `config.yaml` while the page is open, the next WebUI
save — even one that only changes an unrelated field — writes the stale values
back to disk and the database, discarding the hand edit.

**Reproduction:** `/tmp/audit/internal/config/config_probe2_test.go::TestAuditStaleWebUISaveOverwritesHandEdit` mirrors the handler exactly (`in.Merge(m.MustLoad())` then `m.Save(in)`):

```bash
cd /tmp/audit && /usr/local/go/bin/go test ./internal/config/ -run TestAuditStaleWebUISave -v
# URL after save = "https://api.fuzy.site"
# BUG: the hand-edited URL was overwritten by a WebUI save that did not touch it
```

**Impact.** The headline promise of the two-copy design — "a change to the file
wins on the next read" (`config.go:5-9`) — is defeated for any edit made while
the panel is open. Worse, because `saveConfig` calls `Merge(r.cfg.MustLoad())`
first, that `MustLoad()` *adopts* the hand edit into the database and then
`Save` overwrites it with the stale form values, so both copies end up reverted
and the hand edit is gone from both. On a long-lived admin tab this quietly
reverts connection changes.

---

## Finding 6 — CONFIRMED — A failed auth check is swallowed and the UI hangs on "载入中…" forever

**File:** `web/src/App.tsx:26-32` (`checkAuth`) and `App.tsx:61-67` (the
`if (!auth)` gate).

**What is wrong.** `checkAuth` sets `err` on failure but never sets `auth`:

```ts
const checkAuth = useCallback(async () => {
  try { setAuth(await api.authState()) }
  catch (e) { setErr(String(e)) }          // auth stays null
}, [])
```

The render gate is `if (!auth) { return <载入中…/> }`. Because the error banner
lives *inside* the authenticated branch (`App.tsx:121`), it is never shown when
`auth` is still `null` — so any failure of `GET /api/auth/state` leaves the app
permanently on the "载入中…" spinner with no message and no retry. The same
applies to `Login.tsx`'s `onDone={checkAuth}`: a post-login state re-fetch
failure strands the user on the loading screen even though they are now logged
in.

**Reproduction** (real browser, Chromium via CDP). Serve `web/dist` with a stub
whose `/api/auth/state` returns 500:

```bash
python3 /tmp/audit/stubserver.py &   # serves web/dist, /api/auth/state -> 500
# then open http://127.0.0.1:4199/
```

Result:

```
body text: 载入中…
root html: <div class="flex min-h-full items-center justify-center p-4 text-xs text-muted-foreground">载入中…</div>
```

The page never progresses past the spinner; the error is captured in state but
unreachable. (It stays this way indefinitely — no timer, no retry.)

**Impact.** A backend hiccup, a reverse-proxy 5xx, or a transient network error
on first load bricks the UI until a manual reload; the operator gets a bare
spinner instead of the actual error, which is exactly the failure mode that
makes a "the panel won't load" report undiagnosable. A single failed request
should show the error and offer a retry, not hide it behind a gate that can
never be satisfied.

---

## Finding 7 — CONFIRMED — An expired session leaves the app rendered as a dead panel instead of returning to the login screen

**File:** `web/src/api.ts:77-94` (`req` throws `Error("未登录")` on 401),
`web/src/App.tsx:69-71` (the `!auth.authenticated` gate) and `App.tsx:34-41`
(`refresh`).

**What is wrong.** `authState` is fetched once on mount. If the session cookie
expires (7-day TTL, `internal/api/auth.go:23`) or is invalidated, the next
authenticated call returns 401 and `req` throws — but nothing transitions the
app back to the login form. `auth.authenticated` stays `true`, so the shell
(header, tabs, username, 退出) keeps rendering; `bootstrap` fails, so the
"已配置" badge and group count go blank; and every panel shows only whatever
inline error the failing call produced. The only way out is the manual 退出
button.

**Reproduction** (real browser). Stub returns `authenticated: true` from
`/api/auth/state` but 401 for every other `/api/*` call (expired session):

```bash
python3 /tmp/audit/stubserver3.py &   # :4201
# open http://127.0.0.1:4201/
```

Result — the full panel renders with a lone error and no path to re-login:

```
body text: QQBot 群监测 … admin … 退出 … 概览 群管理 用户管理 分组转换 监测设置 连接配置
          未登录 ✕
          状态 群成员 0 已绑定 0 今日已签到 0 今日已发放 0
          统计日期 -　·　监测群 0 个
still shows app chrome (退出 button)? True
```

**Impact.** On an expired session the operator sees an apparently-live panel
full of zeros and a cryptic "未登录" note, with no redirect and no re-auth
prompt. Any action they take (sync, bind, save) fails the same way. The correct
behaviour — detect the 401 and drop back to `<Login>` — is missing, so the
session-expiry path is a dead end for a non-technical operator.

---

## Items checked and found clean

- **`flexInt` (`internal/napcat/flexint.go`).** Correct for the real payload
  shapes: `"7"`→7, `7`→7, `""`→0, `null`→0, a non-numeric string→0, and it
  rejects a float/bool. Verified by `TestAuditFlexInt` and
  `TestAuditMemberFlexLevels` (member list with string `level` + numeric
  `qq_level` decodes). The class of bug in Finding 1 does not recur here because
  the *level* fields never carry fractional values, whereas `rank` does.
- **OneBot response decoding / retcode handling (`decodeAction`,
  `internal/napcat/client.go:123-141`).** retcode ≠ 0 is an error, retcode 0 with
  an empty list is a success, and the message falls back to `wording`. Matches
  the existing tests; no misread found.
- **NapCat WebUI login (`internal/napcat/client.go:289-307`).** The hash is
  `sha256(token + ".napcat")` hex-encoded, exactly matching NapCat's
  `AuthHelper.generatePasswordHash`
  (`reference/NapCatQQ/packages/napcat-webui-backend/src/helper/SignToken.ts:101-103`),
  the `require2FA` branch is handled, and a missing `Credential` is an error.
  The `webuiEnvelope` `{code,message,data}` shape matches `sendSuccess`/`sendError`.
  Clean.
- **`newapi.AddQuota` (`client.go:210-220`).** Sends `{action:"add_quota",
  mode:"add", value>0}` to `/api/user/manage`, which is the `manageUserQuota`
  contract (`reference/new-api/controller/user_quota.go`). Rejects non-positive
  values. Correct.
- **`newapi.SetGroup` (`client.go:196-206`).** The PUT body is
  `{id,username,display_name,role,group,remark}` — it includes `display_name`
  (so a sparse write cannot blank it) and deliberately omits `email` (the
  binding is owned locally). Verified by `TestAuditSetGroupBody`:
  `map[display_name:Alice group:vip id:5 remark:r role:1 username:alice]`.
  Consistent with the "re-read by id, resend what persists" invariant.
- **`Config.Redacted`/`Merge` secret handling (`config.go:293-327`).** The
  redaction placeholder round-trips and secrets survive a re-save; covered by the
  existing `TestRedactedAndMergeProtectSecrets`. (Finding 5 is about the
  *non-secret* fields, which Merge does not restore.)
- **Theme switch (`web/src/theme.tsx`, `web/index.html`).** Persists and does not
  flash. Verified in the browser: toggling writes `qqbot.theme` to
  `localStorage`; on reload the inline `<head>` script (present in the built
  `dist/index.html`) sets `<html>.dark`/`colorScheme` before the bundle runs, so
  the first paint is already correct in both directions (dark→dark, light→
  blank-class/light). No stale-flash found.
- **`Login.tsx` validation.** Username trim, 8-char minimum and confirm-match on
  setup, non-empty password on login — all client-side checks agree with the
  server (`internal/api/auth.go`), so the form cannot submit data the server
  rejects on these grounds.
- **`Rules.tsx` save path.** No swallowed error on save: `save()` catches and
  calls `onFail`, and the dialog stays open so the operator can correct and
  retry. `deleteRule`/`run` likewise surface failures. The only swallowed
  frontend error found is the group-picker `.catch(() => setGroups([]))`
  (Finding 2), which is where the visible symptom of that bug lands.
- **Duplicate fetches on mount.** The child panels re-run their `load` effect
  because `onFail` is a fresh function on each `App` render (observed: `/api/members`
  fetched 2–3× on a cold load). This is a harmless duplicate request, not a
  correctness bug, and the error path does not loop (verified: counts settle and
  do not grow over time).

---

## Summary

**CONFIRMED: 7 — SUSPECTED: 0.**

The two client decoders are the most damaging. `GetGroupSignedList` rejects the
entire sign-in board as soon as any signer has an even rank — which is every
board with two or more signers — so check-ins are never recorded and rewards are
never paid, silently (Finding 1). `newapi.Groups()` cannot parse the gateway's
default group list (the `"auto"` string ratio), so the rule group pickers are
empty and the error is swallowed in the UI (Finding 2). `FindUserByEmail` reads
only page 1 of a paginated search, so a real exact match is reported as "not
found" and the member is stuck unbound (Finding 3). On the config side, an empty
or comment-only `config.yaml` silently wipes the stored connection settings
(Finding 4), and a stale WebUI save overwrites a hand edit to any field the form
did not touch, defeating the file-wins rule (Finding 5). In the frontend, a
failed `authState` hangs the app on "载入中…" forever (Finding 6), and an expired
session leaves the panel rendered and unusable instead of returning to login
(Finding 7). No `SUSPECTED` findings; `flexInt`, the OneBot retcode handling, the
NapCat WebUI login, `AddQuota`/`SetGroup`, the theme switch and the `Login.tsx`
validation were checked and are clean.
