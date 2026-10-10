# Audit B — adversarial verification of the fixes (verify-B)

Scope: verify that the fixes in `65a9f36` (auth) and `81b9be1` (scheduler/events/webui)
actually close findings B1–B8 of `docs/audit/B-api-events-scheduler.md`, and hunt for
regressions the fixes introduced. Read-only: **no file under `internal/`, `cmd/` or
`web/` was modified.** Probes are injected with `go test -overlay` (mapped to
`/tmp/verify/*_probe_test.go` via `/tmp/verify/overlay.json`), exactly as the original
audit did, so nothing in the repo's tree is touched.

Method: every verdict below is decided by an executed probe, not by reading alone.
Probes were run without `-race` (fast) and again with `-race` where concurrency mattered.

Baseline (unmodified tree): `go build ./...` OK; `go test ./internal/...` all `ok`;
`go test -race ./internal/... -count=1` all `ok`; `go vet ./internal/...` clean;
`gofmt -l internal/ cmd/` empty.

> **Note on a concurrent change.** While this verification ran, a separate process modified
> `internal/store/store.go` + `store_test.go` (added SQLite `_pragma` DSN options, WAL, and
> `SetMaxOpenConns(1)`). That is not mine — I only wrote to `/tmp/verify/` and
> `docs/audit/verify-B.md`, and did not touch `internal/`, `cmd/` or `web/`. All probes below
> were **re-run against the current tree** (with that change present) and every result is
> unchanged; the single-connection pool does not mask the generation race (a Go-level
> read-modify-write between two `GetSetting`/`PutSetting` round-trips, not a SQL race).

---
## B1 — VERIFIED (session revocation works), with a NEW defect in the init path

**Command**
```bash
cd /home/QQbot
export PATH=$PATH:/usr/local/go/bin
go test -overlay=/tmp/verify/overlay.json -run 'TestVerifyB1|TestVerifyB3|TestVerifyB4' \
  ./internal/api/ -v -count=1 -timeout 300s
```
**Observed (excerpt)**
```
TestVerifyB1_LogoutRevokesCapturedCookie
    replay-after-logout status = 401
TestVerifyB1_PasswordChangeRevokes
    pre-change cookie after password change = 401
TestVerifyB1_LogoutIsGlobalAcrossClients
    client B after client A logs out = 401 (401 == global logout)
TestVerifyB1_NoGenerationRowStillWorks
    old deployment: generation()=1 persisted=1
TestVerifyB3_LoginTimingGapGone
    known-user avg=86.198745ms (status=401)  unknown-user avg=90.453621ms (status=401)
TestVerifyB3_DummyHashIsValid
    dummyHash cost=10 realHash cost=10
TestVerifyB4_DottedUsernames ... PASS
TestVerifyB4_MalformedCookiesRejectedCleanly ... PASS
```

**Verdict — VERIFIED.** The captured cookie is rejected after an authenticated logout
(401) and after a password change (401); a fresh login afterwards succeeds (200), so the
generation bump does not lock the real user out. An "old deployment" that has an admin
but no `auth.generation` row is handled: `generation()` self-initialises to 1, persists it,
and is stable across calls.

**Adversarial notes (no false pass):**
- The generation is read on **every** authenticated request (`currentUser` → `generation()`),
  i.e. one extra `settings` SELECT per request. It is cheap (primary-key lookup) and correct;
  acceptable for a single-operator panel. Worth stating, not a bug.
- The generation is **global**, so one authenticated logout signs out *every* client, not
  just the caller's browser — a behaviour change from per-browser logout. Arguably intended
  ("log out everywhere"), but it is a UX change the commit message does not mention.
- `bumpGeneration` is a non-atomic read-modify-write. Under concurrent logins/logouts a bump
  can be lost — see **New bug #1** for the one case where that leaves a live cookie.

---
## B2 — VERIFIED

**Command**
```bash
go test -overlay=/tmp/verify/overlay.json -run 'TestVerifyB2' ./internal/api/ -v -count=1
go test -race -overlay=/tmp/verify/overlay.json -run 'TestVerifyB2' ./internal/api/ -v -count=1
```
**Observed**
```
TestVerifyB2_ConcurrentSetupOneAdmin
    admins=1 success=1 conflict=11 other=0
TestVerifyB2_SecondSetupRefused
    second setup -> 409 {"error":"已完成初始化"}
TestVerifyB2_SetupLockDoesNotBlockLogin
    login during setup burst: status=200 latency=89.813844ms
```
(With `-race`: same result, `admins=1 success=1 conflict=11 other=0`, no race report.)

**Verdict — VERIFIED.** 12 concurrent setups produce exactly 1 admin and exactly 1 success;
the other 11 are refused with 409. A sequential second setup is refused with 409. The
`setupMu` mutex serialises only the setup path — a login issued while a 12-way setup burst
is in flight completes normally in ~90 ms, so the lock does not hold the server hostage.
The lock is held across `hashPassword` (bcrypt ~80 ms), which is fine here: setup runs at
most once and nothing else takes the lock.

---
## B3 — VERIFIED

**Command**
```bash
go test -overlay=/tmp/verify/overlay.json -run 'TestVerifyB3' ./internal/api/ -v -count=1
```
**Observed**
```
TestVerifyB3_LoginTimingGapGone
    known-user avg=86.198745ms (status=401)  unknown-user avg=90.453621ms (status=401)
    known body="{\"error\":\"用户名或密码错误\"}\n"  unknown body="{\"error\":\"用户名或密码错误\"}\n"
TestVerifyB3_DummyHashIsValid
    dummyHash cost=10 realHash cost=10
```
**Verdict — VERIFIED.** The original ~150× gap (90 ms vs 0.6 ms) is gone: known-user and
unknown-user now average 86 ms and 90 ms respectively (unknown even slightly *slower*, i.e.
indistinguishable within noise). Both return the identical 401 body. The dummy hash is a
genuine bcrypt hash (`bcrypt.Cost` parses it, cost 10) and matches the real hash cost, so the
constant-time path is real, not a no-op: `checkPassword("")` runs a full cost-10 comparison and
returns false.

---
## B4 — VERIFIED

**Command**
```bash
go test -overlay=/tmp/verify/overlay.json -run 'TestVerifyB4' ./internal/api/ -v -count=1
```
**Observed** — both probes PASS. Dotted usernames `admin.ops`, `first.last`, `a.b.c` (plus
`..` and `user.name.here`) all complete setup → authenticated request (200) and login →
authenticated request (200). Malformed/tampered cookies are all rejected with 401, none panic:

| cookie value | result |
|---|---|
| old 3-part `admin.9999999999.deadbeef` | 401 |
| 2-part `admin.9999999999` | 401 |
| empty / `....` / 5-part `a.b.c.d.e` | 401 |
| non-base64 user `!!!.…` | 401 |
| bad expiry `YWRtaW4.abc.1.deadbeef` | 401 |
| bad generation `YWRtaW4.9999999999.xyz.deadbeef` | 401 |
| tampered signature | 401 |

**Verdict — VERIFIED.** The base64 username fixes the dotted-username lockout, and an
old-format cookie is rejected cleanly (401), not a panic. The separator claim holds:
`base64.RawURLEncoding` emits `[A-Za-z0-9_-]` and hex is `[0-9a-f]`, neither contains `.`,
so `strings.Split(value, ".")` always yields exactly 4 parts for a well-formed cookie.

---
## B5/B6 — VERIFIED

**Command**
```bash
go test -overlay=/tmp/verify/overlay.json -run 'TestVerifyB6|TestVerifyB7' ./internal/scheduler/ -v -count=1 -timeout 300s
```
**Observed (excerpt)**
```
TestVerifyB6_SafelyConvertsPanic
    contained panic error: panic: ...
TestVerifyB6_LoopSurvivesEveryTickPanic
    panicking sweep invoked 5 times; loop kept running
TestVerifyB6_RealPanicContainedAndLoopContinues
    real panic contained: panic: simulated sign-in board client panic
```
**Verdict — VERIFIED.** `Scheduler.safely` converts a panic into a non-nil error carrying a
stack (`goroutine …`). A sweep that panics on *every* invocation ran 5 times over ~21 s
without killing the loop or the test binary (pre-fix, the first panic terminated the
process — see the original finding's `FAIL … 20.071s`). The panic is also recorded in
`Status` (`last_error`). A panic raised through the **real** sweep path (`SyncAllCheckins` →
client) is contained the same way.

*Minor coverage gap (not counted as a bug):* the interval callback `every()` is called
directly in `loop` (scheduler.go:138/149), outside `safely`. `memberInterval`/`ruleInterval`/
`checkinInterval` only read settings and fall back to constants on error, so a panic there is
not currently reachable — but the recovery does not wrap that call.

---
## B7 — VERIFIED

**Command**
```bash
go test -overlay=/tmp/verify/overlay.json -run 'TestVerifyB7' ./internal/scheduler/ -v -count=1 -timeout 300s
```
**Observed**
```
TestVerifyB7_RealDeadBoardIsVisible
    checkins status after first tick: map[last_error:group 42: no sign-in board last_run:1791641698]
TestVerifyB7_HealthyBoardHasNoError
    checkins status after first tick: map[last_run:1791641719]
```
**Verdict — VERIFIED.** Driving the real `Scheduler.Run` against a store whose enabled group's
board always errors, the `checkins` entry now carries `last_error` alongside `last_run` — the
award step's nil return no longer erases it. The companion probe confirms the fix does not
over-correct: a healthy board leaves **no** `last_error`, so a real outage is still
distinguishable from a clean sweep.

---
## B8 — VERIFIED

**Command**
```bash
go test -overlay=/tmp/verify/overlay.json -run 'TestVerifyB8' ./internal/events/ -v -count=1
go test -race -overlay=/tmp/verify/overlay.json -run 'TestVerifyB8' ./internal/events/ -count=1
```
**Observed**
```
TestVerifyB8_HandleSafelyContains ... PASS
    (raw l.Handle panics as expected; l.handleSafely does not)
TestVerifyB8_ListenerSurvivesPanickingEventOverWS
    [events] recovered from a panic while handling an event: boom deep in the bind path
    [events] bound 222 via >>绑定<<
    bound after a panicking event: [222]
```
**Verdict — VERIFIED.** Over a real WebSocket, an event that panics inside `BindByCommand` is
recovered and logged, the connection stays up, and the *next* event is dispatched normally
(the second bind is recorded). The negative control confirms `handleSafely` is load-bearing:
raw `Handle` still panics, so the wrapper is not dead code. No race reported with `-race`.

---
## New bugs introduced by the fixes

### New bug #1 — `generation()` init is a lost-update race; a cookie can survive the logout it raced with

**File:** `internal/api/auth.go:63-84` (`generation`, `bumpGeneration`).

`generation()` does read→(if 0) write-1, and `bumpGeneration()` does `generation()` then
write `g+1`. Both are non-atomic read-modify-writes on the same settings row. On a deployment
that has **no `auth.generation` row yet** (the state right after upgrading to this code), a
logout that races the first request which initialises the row can lose its increment entirely:
both read 0, both write 1, and the logout's revocation vanishes. A session cookie issued in
that same window (signed at generation 1) then remains valid after the logout.

**Command**
```bash
go test -overlay=/tmp/verify/overlay.json -run 'TestVerifyNew' ./internal/api/ -v -count=1
```
**Observed**
```
TestVerifyNew_InitBumpRaceLetsCookieSurviveLogout
    trials=200: cookie issued concurrently with a logout STILL valid afterwards in 43 cases
    NEW BUG: a cookie survives the logout it raced with (generation bump lost on init)
TestVerifyNew_InitBumpLostUpdate
    trials=200: final generation < 2 (a bump was lost) in 16 cases
TestVerifyNew_SequentialGenerationDeterministic ... PASS (1 -> 2)
```
**Impact.** Exactly the B1 defect, confined to the window after an upgrade (or first run)
before the `auth.generation` row exists: a captured cookie can outlive a logout or password
change. Once the row exists the read-modify-write only loses a *concurrent* increment, which
no longer leaves a valid cookie (any prior cookie is still one generation behind). **Fix
direction:** make the first write a conditional insert / `UPDATE … SET value = value + 1` and
treat a lost initialiser as a bump (e.g. use the store to increment atomically, or gate init
behind a mutex/`ON CONFLICT DO UPDATE`).

### New bug #2 — unauthenticated `POST /api/auth/logout` revokes the operator's session (credential-free DoS)

**File:** `internal/api/api.go:47` (logout is registered without `requireAuth`) +
`internal/api/auth.go:335-347` (`logout` now calls `bumpGeneration` unconditionally).

Before the fix, `logout` only cleared the caller's own cookie, so an unauthenticated caller
had no effect on anyone else. Now it bumps the **global** generation with no auth check, so any
unauthenticated request to `/api/auth/logout` signs the real operator out of every device. There
is no rate limiting, so this is a trivial availability attack against the panel.

**Command**
```bash
go test -overlay=/tmp/verify/overlay.json -run 'TestVerifyNew_UnauthenticatedLogout' ./internal/api/ -v -count=1
```
**Observed**
```
TestVerifyNew_UnauthenticatedLogoutRevokesAdmin
    unauthenticated logout -> 200 {"ok":true}
    operator's session after an unauthenticated logout = 401 (401 == kicked out)
    NEW BUG: an unauthenticated request to /api/auth/logout revoked the operator's session
TestVerifyNew_LogoutKicksAllDevices
    device B after device A logs out = 401   (also: logout is global)
```
**Impact.** An unauthenticated attacker can log the admin out at will, repeatedly. It does not
grant access, but it is a denial-of-service that did not exist before and contradicts the
`AGENTS.md` invariant that only `auth/state|setup|login|logout` are reachable without a
session *and* harmless. **Fix direction:** only bump the generation when the request carries a
valid session (i.e. move the bump inside an authenticated branch, or `requireAuth` the route
and keep a separate "clear cookie" for the no-session case).

---
## Summary

**VERIFIED: 7 — REFUTED: 0 — NEW BUGS: 2.** Every fix B1–B8 does what it claims, proven by
executed probes, and the repo's own suite plus `-race` stay green. Two defects were introduced
by the B1 fix, both in the new generation machinery: a lost-update race during generation
initialisation that can let a cookie survive a raced logout on an upgraded instance, and an
unauthenticated `logout` that now globally revokes the operator's session (DoS). Neither
reopens the original B1 finding in the steady state; both should be fixed before this branch
lands.

Evidence files (outside the repo tree): `/tmp/verify/auth_probe_test.go`,
`/tmp/verify/auth_race_probe_test.go`, `/tmp/verify/auth_logout_probe_test.go`,
`/tmp/verify/scheduler_probe_test.go`, `/tmp/verify/events_probe_test.go`,
`/tmp/verify/overlay.json`. Report: `docs/audit/verify-B.md`.
