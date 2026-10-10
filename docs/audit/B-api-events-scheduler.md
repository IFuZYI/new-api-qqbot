# Audit B — API, auth, events, scheduler

Scope: `internal/api/{api,auth,rules,static}.go`, `internal/events/listener.go`,
`internal/scheduler/scheduler.go`. Read-only on the repo.

Every finding below was reproduced with a throwaway in-package test injected via
`go test -overlay` (no file under `internal/` or `cmd/` was modified). Probe
sources live in `/tmp/audit/*_probe_test.go`, mapped by
`/tmp/audit/overlay.json`; run e.g.:

```bash
cd /home/QQbot
/usr/local/go/bin/go test -overlay=/tmp/audit/overlay.json -run TestAudit ./internal/api/ -v
```

Route inventory (item 2 of the brief): all 26 routes registered in
`Router.routes()` were probed without a session — see Finding 7.

---

## Finding 1 — CONFIRMED — Sessions are stateless and never revoked (logout and password change do not invalidate a cookie)

**File:** `internal/api/auth.go:54-60` (`signSession`), `auth.go:63-83`
(`verifySession`), `auth.go:136-144` (`requireAuth`), `auth.go:239-244`
(`logout`), `auth.go:246-279` (`changePassword`).

**What is wrong.** The session is a self-contained signed token
(`username.expiry.mac`) with no server-side record and no per-user generation
counter. `verifySession` only checks the HMAC and the expiry, so a token stays
valid until `expiry` (7 days) no matter what happens to the account. `logout`
merely clears the cookie in the caller's browser; it does not — and cannot —
invalidate the value. `changePassword` rewrites the hash but leaves every
outstanding cookie valid. The signing key (`auth.session_key`) is only ever
created, never rotated, so even a full password reset does not sign existing
tokens out.

**Reproduction.**
`/tmp/audit/api_probe_test.go::TestAuditLogoutDoesNotInvalidateCookie` captures
the cookie, calls `POST /api/auth/logout`, then replays the captured value:

```
--- FAIL: TestAuditLogoutDoesNotInvalidateCookie
    BUG: the cookie is still valid after logout (server-side sessions are never revoked)
```

`TestAuditSessionSurvivesPasswordChange` changes the password and replays the
pre-change cookie:

```
--- FAIL: TestAuditSessionSurvivesPasswordChange
    BUG: a session issued before the password change still authenticates afterwards
```

**Impact.** Any captured/stolen cookie (e.g. from a shared or compromised
browser, a proxy log, or a leaked `Set-Cookie`) grants full admin access for up
to 7 days. The victim cannot recover: logging out, changing the password, or
being told to "log out everywhere" has no effect on the attacker's copy. There
is no way to revoke a session short of deleting the `auth.session_key` setting
(which logs everyone out) — and nothing in the UI does that.

---

## Finding 2 — CONFIRMED — First-run setup is a TOCTOU gate; concurrent requests create multiple admins

**File:** `internal/api/auth.go:165-210` (`setup`), specifically the
check-then-create at `auth.go:166-174` (`needsSetup`) followed by
`auth.go:198` (`CreateAdmin`).

**What is wrong.** `setup` reads `CountAdmins()` and then, in a separate step,
inserts the admin. There is no transaction, no unique constraint on "the first
admin", and no lock. Concurrent `POST /api/auth/setup` requests all observe
`needsSetup == true` and all succeed. The `admins.username` column is
`uniqueIndex`, but the requests use different usernames, so that does not help.

**Reproduction.** `TestAuditSetupRaceCreatesMultipleAdmins` fires 12 concurrent
setup calls against a fresh instance:

```
setup responses 200: 10, admins in DB: 10
BUG: 10 admins were created by concurrent setup requests
```

**Impact.** The "first-run gate" — the one thing that stops an unauthenticated
caller from taking over an *installed* instance — can be raced. In the window
between the operator's own setup and this one (the two can overlap if the panel
is exposed before the operator finishes), an attacker who fires a burst of setup
requests gets an admin of their choosing and can then log in with it. The same
burst can also be aimed at a *fresh* instance that is briefly reachable before
the legitimate operator reaches it, permanently occupying it with attacker
credentials (the operator's later `setup` still succeeds, creating a second
admin, but the attacker's account remains).

---

## Finding 3 — CONFIRMED — Login leaks username existence through timing (user enumeration)

**File:** `internal/api/auth.go:212-237` (`login`).

**What is wrong.** On a login attempt the code calls
`GetAdmin(username)` and then, only if the admin exists, runs
`checkPassword` → `bcrypt.CompareHashAndPassword`. bcrypt at `DefaultCost` is
intentionally ~60-100 ms. For an unknown username, `GetAdmin` returns an error,
`a` is a nil `*Admin`, and the short-circuit `err != nil || !checkPassword(...)`
never runs bcrypt. So a request for a non-existent user returns in well under a
millisecond while a request for an existing user returns in ~tens of
milliseconds. The identical error *message* (the comment at `auth.go:224-225`)
hides the username in the body, but not in the response time.

**Reproduction.** `TestAuditLoginTimingLeaksUserExistence` averages 8 wrong-password
attempts per username:

```
known-user=90.625993ms unknown-user=589.775µs
BUG: login latency distinguishes an existing username (90.625993ms) from an unknown one (589.775µs)
```

The ~150x gap is unambiguous and needs no statistics.

**Impact.** An unauthenticated attacker can enumerate valid admin usernames by
timing, then concentrate a credential-stuffing/brute-force effort on the real
account. Combined with the fact that login has no rate limiting, lockout, or
delay, this materially lowers the cost of guessing the password.

---

## Finding 4 — CONFIRMED — A username containing `.` produces a session that can never authenticate

**File:** `internal/api/auth.go:55-60` (`signSession`) and `auth.go:63-83`
(`verifySession`); accepted by `setup` (`auth.go:184-188`) and `login`
(`auth.go:222`).

**What is wrong.** The cookie payload is `username + "." + expiry` and the MAC
covers exactly that string, but `verifySession` re-derives the payload by
joining the *first two* dot-separated parts:

```go
parts := strings.Split(value, ".")
if len(parts) != 3 { return "", errors.New("malformed session") }
payload := parts[0] + "." + parts[1]
```

`signSession` never restricts the username, and `setup` only trims whitespace.
If the username contains a `.` (e.g. `admin.ops`, `first.last`), the signed
value has more than three parts, `verifySession` rejects it, and
`currentUser` returns `""`. The consequence is that login returns HTTP 200 and a
`Set-Cookie`, but every subsequent authenticated request is 401.

**Reproduction.** `TestAuditUsernameWithDotLocksOut` runs setup with
`admin.ops`, then immediately calls `/api/bootstrap` with the cookie setup just
set:

```
BUG: session issued by setup is rejected (bootstrap=401)
BUG: login returned 200 but the session is rejected (bootstrap=401)
```

**Impact.** Self-inflicted denial of service: an operator who picks a dotted
username (a common convention) creates an instance they can never log into, and
the failure is silent (200 + cookie, then 401s). On an installed instance the
only recovery is manual DB surgery. (A secondary, milder note: any cookie whose
username segment happens to contain a dot also fails closed — the parsing is
inconsistent between sign and verify.)

---

## Finding 5 — CONFIRMED — `Award` uses an unsynchronised `*rand.Rand` shared by the scheduler and the WebUI

**File:** `internal/service/service.go:79` (`rnd *rand.Rand`), `service.go:402-407`
(`draw` → `s.rnd.Intn`), `service.go:349-399` (`Award`). Reached concurrently by
`internal/scheduler/scheduler.go:54-65` (the `checkins` loop) and
`internal/api/api.go:415-421` (`POST /api/actions/award`).

**What is wrong.** `*math/rand.Rand` is not safe for concurrent use. The
service holds a single instance and `draw` mutates its internal `rngSource`.
The scheduler's check-in loop calls `Award` on its own goroutine while an
operator can trigger the award action over HTTP on a `net/http` handler
goroutine at the same time, so two goroutines call `s.rnd.Intn` concurrently.

**Reproduction.** `TestAuditConcurrentDrawRacesOnRand` (16 goroutines, `-race`)
reports:

```
WARNING: DATA RACE
Write at ... math/rand.(*rngSource).Uint64()
  ... qqbot/internal/service.(*Service).draw() service.go:406
Read at ... math/rand.(*rngSource).Uint64()
  ... qqbot/internal/service.(*Service).draw() service.go:406
```

**Impact.** A data race on the RNG state is undefined behaviour: in practice it
can produce biased or correlated draws, and it is a latent crash surface. It is
also the kind of defect `go test -race` exists to catch, so a `-race` CI run on
the real award path would flag it. (The monetary risk is bounded because the
claim (`MarkRewarded`) is still atomic, but the drawn amount itself is
corrupted.)

---

## Finding 6 — CONFIRMED — A panic in any scheduler sweep is unrecovered and kills the whole process, silently stopping every future sweep

**File:** `internal/scheduler/scheduler.go:121-152` (`loop`), invoked as
`go s.loop(...)` at `scheduler.go:38-65`. No `recover()` anywhere in
`scheduler.go`.

**What is wrong.** Each patrol runs in its own goroutine and calls `fn()`
directly. Nothing recovers a panic. A panic on a background goroutine is not
caught by `net/http`'s per-request recovery, so it terminates the entire
process. Because the sweep code fans out into external clients (new-api HTTP,
NapCat HTTP/WebSocket, JSON decoding), any single unhandled nil-deref or
similar there takes the monitor down — and because it is a background goroutine
there is no request to correlate it with.

**Reproduction.** `TestAuditSweepPanicKillsProcess` drives a `loop` whose `fn`
panics; the test binary is killed by the runtime:

```
=== RUN   TestAuditSweepPanicKillsProcess
panic: simulated sweep bug (e.g. nil deref inside a client)
goroutine 5 [running]:
qqbot/internal/scheduler.(*Scheduler).loop(...)
  /home/QQbot/internal/scheduler/scheduler.go:139
FAIL  qqbot/internal/scheduler  20.071s
```

(No test assertion ran; the process died — that is the finding.)

**Impact.** One bug in any sweep is a full outage: the WebUI, the event
listener and the other two patrols all stop, and nothing restarts them (the
process is only brought up by whatever supervises it). The brief's worry — "a
panic in a sweep cannot kill the process or silently stop all future sweeps" —
is exactly what happens: it does both.

---

## Finding 7 — CONFIRMED — `Status` never reports a failed check-in board sync (the error is erased in the same tick)

**File:** `internal/scheduler/scheduler.go:54-65` (the `checkins` sweep closure)
and `scheduler.go:154-163` (`record`).

**What is wrong.** When `SyncAllCheckins` fails (e.g. a group with no sign-in
board — explicitly called "normal" in the comment at `scheduler.go:60`), the
closure records the error, then unconditionally calls `Award` and **returns its
error**, which is normally `nil`:

```go
if err := s.svc.SyncAllCheckins(date); err != nil {
    s.record("checkins", err)      // records the failure
}
_, err = s.svc.Award(date)
return err                          // normally nil
```

Back in `loop` (`scheduler.go:139-144`), a `nil` return calls
`s.record(name, nil)`, which deletes `lastErr["checkins"]`. The failure that was
just recorded is wiped before the UI can ever read it, so `Status()` always
shows `checkins` as healthy even when the board was never read and no rewards
were computed.

**Reproduction.** `TestAuditCheckinSyncErrorIsMaskedInStatus` points the check-in
sweep at a group whose board always errors and reads `Status()` after the first
tick:

```
checkins status: map[last_run:1791639622]
BUG: the check-in board sync failed but Status reports no last_error
```

(`last_run` is present, `last_error` is absent — the UI sees a clean patrol.)

**Impact.** Silent data loss of the operational signal: the operator's dashboard
claims check-ins are running while the sign-in board is unreadable and members
are going unrewarded. This directly undermines the "a group without a board is
normal, not a failure" intent — a *real* board outage is indistinguishable from
a healthy sweep.

---

## Finding 8 — SUSPECTED — No panic recovery on the event-dispatch goroutine; a panic in a bind handler kills the process

**File:** `internal/events/listener.go:141-147` (`session` loop → `l.Handle`),
`listener.go:161-178` (`Handle`), `listener.go:180-224`
(`handleMessage`/`handleGroupIncrease`).

**What is wrong (reasoned, not reproduced end-to-end).** `Handle` runs on the
same goroutine as `Run` (the listener goroutine started in
`cmd/qqbot/main.go:71-74`). There is no `recover()` around `l.Handle(raw)` or
inside it. A malformed or hostile event is defensively validated — unknown
`post_type`, missing/zero `group_id`/`user_id`, non-JSON, non-object JSON are
all ignored (I verified none of these panic; see the negative result below) —
but any panic that originates *below* `Handle`, inside `BindByCommand` →
`ApplyOnBindRules` → the new-api client, would propagate up and terminate the
process. The scheduler findings show the codebase does not add blanket
`recover()` at this layer, so the listener inherits the same failure mode as
Finding 6.

**Reproduction attempt (negative for the malformed-input half).**
`TestAuditHostileEventsDoNotPanic` feeds `null`, `[]`, a bare string, a number,
empty/`nil` bytes, a string-typed `group_id`, a number-typed `raw_message`,
`group_increase` with `group_id`/`user_id = 0`, and a full valid event — none
panic, none produce an unintended write:

```
--- PASS: TestAuditHostileEventsDoNotPanic
```

I did not construct a panic through the real `Service` because doing so would
require an external-client fault that the fakes do not model; the claim is that
*no layer recovers*, which the grep for `recover()` (only matches in
`reference/`) confirms.

**Impact.** If it fires, the same as Finding 6: the whole process (WebUI +
all patrols) goes down on one bad event, though here the event must first pass
the validation above, so the surface is smaller.

---

## Items checked and found clean

- **Route auth coverage (brief item 2).** All 26 routes from
  `Router.routes()` were probed without a session; every one returned 401.
  `TestAuditEveryRouteRequiresAuth` PASSES. No mutating endpoint is missing
  `requireAuth`.
- **`static.go` path traversal (brief item 5).** `filepath.Clean("/" + path)`
  strips `..` before `filepath.Join`, and `http.FileServer`/`http.ServeFile`
  re-reject any residual traversal. `TestAuditStaticPathTraversal` tried
  `/../secret.txt`, `..%2f`, `%2e%2e`, nested `assets/../../`, and `....//` —
  none escaped the web root. Clean.
- **Event dispatch malformed/hostile input (brief item 4, input half).** See
  Finding 8: no panic and no unintended write from any malformed payload tried.
- **Scheduler busy-loop / missed timer reset / leak (brief item 3, timing
  half).** `loop` uses a single `time.Timer`, always re-armed via
  `timer.Reset` (never `time.After` in a `select` loop), and the disabled path
  resets to `patrolDisabledCheck` (30 s), so there is no busy-loop and no
  per-tick allocation leak. `Run` returns on `ctx.Done()`. The interval is
  re-read every tick as intended.
- **Listener shutdown while the socket is open (brief item 4, shutdown half).**
  `TestAuditRunReturnsOnCancelWhileConnected` cancels the context while a real
  WebSocket is open; `Run` returns within the timeout. The `conn.Close()` in
  the watcher goroutine unblocks `ReadMessage`. Clean.

---

## Summary

**CONFIRMED: 7 — SUSPECTED: 1.**

Most serious: the session model has no revocation at all (Finding 1) — a
captured cookie survives logout and password change for 7 days, and there is no
recovery path — and the first-run setup gate can be raced to plant an
attacker-controlled admin (Finding 2). An unauthenticated attacker can also
enumerate admin usernames by login timing (Finding 3, ~150x gap), and an
operator who picks a dotted username is permanently locked out (Finding 4).
On the runtime side, any panic in a scheduler sweep (Finding 6) or, suspected,
the event-dispatch path (Finding 8) is unrecovered and kills the whole process,
and a failed check-in board sync is silently reported as healthy (Finding 7).
A `-race` run flags the shared RNG in `Award` (Finding 5). No auth bypass was
found in the route table, no state-mutating endpoint lacks `requireAuth`, and
`static.go` has no path traversal.
