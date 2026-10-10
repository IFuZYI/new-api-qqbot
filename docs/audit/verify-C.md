# Verify C — adversarial verification of the config/client/frontend fixes

Scope: independently re-check that commits `a0ab232`, `17680e8`, `b37abb9`,
`81b9be1` close Audit C Findings 1–7, and hunt for bugs those fixes introduced.

Method: read the diffs and current sources; run the repo's own tests; and, where
a live-shaped payload is needed, drive the actual (unmodified) code from a
scratch copy under `/tmp/verify-c` so nothing under `internal/`, `cmd/` or `web/`
is touched. Frontend findings are checked in a real browser (Chromium via CDP)
against a stub server that serves the built `web/dist` and scripts `/api/*`.

Baseline (all green):

```bash
cd /home/QQbot && export PATH=$PATH:/usr/local/go/bin && go vet ./... && go test ./internal/...
# ok  qqbot/internal/{api,config,events,napcat,newapi,scheduler,service,store}
cd /home/QQbot/web && npm run build      # tsc -b --noEmit && vite build  -> green
```

Verdicts: **VERIFIED** (fix closes the original defect) / **PARTIAL** / **REFUTED**.

---

## C1 — VERIFIED

Fractional/string sign-in rank (`a0ab232`, `flexFloat`). The live-shaped board
decodes whole and the fraction survives; the service rounds for storage and the
store schema is unchanged.

```bash
cd /tmp/verify-c && /usr/local/go/bin/go test ./internal/napcat/ -run TestVerifyC1 -v
# --- PASS: TestVerifyC1LiveShapedBoardDecodesWhole  (ranks 1, 1.5, 2 -> 3 members, out[1].Rank==1.5)
# --- PASS: TestVerifyC1RankShapesNeverLoseArray    (""/null/"nonsense"/-1.25/2/"2.5"/0 all decode, array never lost)
# --- PASS: TestVerifyC1BadRankFirstKeepsRest       (garbage rank on member 1 keeps members 2 and 3)
# --- PASS: TestVerifyC1BoolRankFailsWholeArray     (err: cannot unmarshal bool into float64)
cd /tmp/verify-c && /usr/local/go/bin/go test ./internal/service/ -run TestVerifyC1 -v
# --- PASS: TestVerifyC1ServiceRoundsRank           (1.5->2, 2.5->3: ROUNDED not truncated)
# --- PASS: TestVerifyC1RoundCollisionAccepted      (ranks that round together are accepted)
```

`store.Checkin.Rank` is still `int` (`internal/store/store.go:66`); the service
converts with `int(math.Round(float64(m.Rank)))` (`internal/service/service.go:220`).
Verdict: the original defect (whole board lost on the first even rank) is closed.

Residual (not the reported case, not blocking): a rank sent as a genuinely
non-numeric JSON **type** (e.g. `true`) still fails the whole array — the same
failure class, just no longer reachable from any real NapCat payload (NapCat
emits a JS number; `GetGroupSignedList.ts:45`). Documented, not a regression.

---

## C2 — VERIFIED

`newapi.Groups()` string ratio (`17680e8`, `ratio json.RawMessage`).

```bash
cd /tmp/verify-c && /usr/local/go/bin/go test ./internal/newapi/ -run TestVerifyC2 -v
# --- PASS: TestVerifyC2AllThreeNames         (default=1, auto="auto", vip=0.5 -> 3 names, no error)
# --- PASS: TestVerifyC2MalformedStillErrors  (data:array / entry:string / success:false all error, no silent map)
# --- PASS: TestVerifyC2WeirdRatioTolerated   (nested-object ratio tolerated)
# --- PASS: TestVerifyC2NullDataSilentEmpty   (data:null -> map[] with err=<nil>)
```

All three names are returned and a genuinely malformed list still errors rather
than returning a silent empty map. Verdict: the reported 502 / empty pickers is
closed. Minor residual: `data:null` now yields an empty map with no error
(silent-empty), and a nested-object ratio is tolerated because `RawMessage` is
lenient — neither is the reported case.

---

## C3 — VERIFIED

`FindUserByEmail` pagination (`17680e8`).

```bash
cd /tmp/verify-c && /usr/local/go/bin/go test ./internal/newapi/ -run TestVerifyC3 -v
# --- PASS: TestVerifyC3ExactOnPage2        (100-row page 1 of decoys, exact match on page 2 -> found id=8)
# --- PASS: TestVerifyC3ShortPageStops      (1 request only for a short page)
# --- PASS: TestVerifyC3CapsAtMaxPages      (full pages forever -> stops at 20, no infinite loop)
# --- PASS: TestVerifyC3AmbiguityStillErrors (two exact matches -> "more than one" error)
# --- PASS: TestVerifyC3CaseInsensitive     (54321@QQ.COM matches 54321@qq.com)
```

Exact match on page 2 is found, the scan stops on a short page, ambiguity still
errors, and a CASE-only difference is still matched. Verdict: closed.

Residual: against a server that *ignores* `page_size` and returns its default 10
rows per page, the loop stops after one page (`len(items) < searchPageSize`)
and the page-2 match is missed. That is not new-api's behaviour — it honours
`p`/`page_size` up to 100 (`reference/new-api/common/page_info.go:41-79`) — so it
is not a live defect.

---

## C4 — VERIFIED (with a new bug in the same path — see N1)

`Config.isZero()` (`b37abb9`).

```bash
cd /tmp/verify-c && /usr/local/go/bin/go test ./internal/config/ -run 'TestVerifyC4Whitespace|TestVerifyC4OneValue' -v
# --- PASS: TestVerifyC4WhitespaceVariants
#     spaces-only: intact (loadErr=<nil>)
#     blank-lines: intact (loadErr=<nil>)
#     tabs:        intact (loadErr=parse ... cannot start any token)   <- malformed path, still no wipe
# --- PASS: TestVerifyC4OneValueIsAdopted   (file with only newapi.url -> adopted)
```

Empty, spaces-only, blank-line-only, comment-only and header-only files all
leave the stored config intact; a file with even one value is adopted. Verdict:
the reported wipe is closed. See **N1** for the partial-file case the fix does
not cover.

---

## C5 — VERIFIED

Revision on GET, required-match on POST, 409 on mismatch (`b37abb9`).

```bash
cd /tmp/verify-c && /usr/local/go/bin/go test ./internal/api/ -run TestVerifyC5 -v
# --- PASS: TestVerifyC5RevisionFlow
#     GET returns a revision; current-revision save -> 200; stale save -> 409 and the
#     concurrent change (two.example.com) is untouched; reload + save -> 200 and applied
# --- PASS: TestVerifyC5EmptyRevisionBypassesGuard
#     no-revision save -> code=200 (guard is opt-in)
```

Frontend plumbing confirmed by reading `web/src/api.ts:146-151` (`getConfig`
returns `{config, revision}`; `saveConfig(c, revision)` sends both) and
`web/src/App.tsx:687-723` (Connection stores `revision` from GET, sends it on
save, and on failure calls `onFail(e)` — which renders the `ErrorNote` banner —
then reloads). The 409 body is the handler's `配置已被修改…` message, which
`api.ts:89-92` turns into the thrown `Error`. Verdict: closed.

Residual (not a regression): the guard is opt-in — a POST with `Revision:""`
skips the check entirely (probe: `code=200`), so a legacy/hostile client can
still clobber. The shipped form always sends the revision, so the reported
stale-tab scenario is covered.

---

## C6 — VERIFIED

Auth-check failure shows the error + retry (`81b9be1`).

Real browser, stub whose `/api/auth/state` returns 500:

```bash
python3 /root/.hermes/cache/scratch/stubserver.py 4199 autherr /home/QQbot/web/dist
# open http://127.0.0.1:4199/
```

```text
BODY: state unavailable\n重试
HAS 载入中: False
HAS 重试: True
ROOT HTML: <div class="...text-center"><div class="...border-destructive..."><span>state unavailable</span></div>
           <button ...>重试</button></div>
```

The error branch renders in the `if (!auth)` block (`App.tsx:74-91`), i.e.
**outside/above** the authenticated gate, driven by `authError`. Clicking 重试
re-runs `checkAuth` and recovers to the shell (stub failing once then
succeeding): after the click the body shows `概览 … 退出 … 已配置 admin`, and
`重试` is gone. Verdict: the infinite `载入中…` is closed.

---

## C7 — VERIFIED

Expired session drops to login (`81b9be1`). The match string is correct: the
server's 401 body is exactly `{"error":"未登录"}` (`internal/api/auth.go:219`,
`writeErr(w, 401, errors.New("未登录"))`), and `api.ts:90` throws
`Error(body.error)` → message `未登录`, which `App.tsx:48` tests with
`msg.includes('未登录')`.

Real browser, stub: `/api/auth/state` → authenticated:true, every other
`/api/*` → 401 `未登录`:

```bash
python3 /root/.hermes/cache/scratch/stubserver.py 4201 expired /home/QQbot/web/dist
# open http://127.0.0.1:4201/
```

```text
BODY: QQBot 群监测\n\n请登录管理后台\n\n用户名\n密码\n登录\n...
HAS 退出(chrome): False
HAS 概览(tab): False
HAS 未登录: False
```

The app lands on the login form, not the dead panel with a lone `未登录` note.
Verdict: closed.

---

## New bugs found

### N1 — A partial config file wipes the stored fields it omits (config)

Same code path as C4, not closed by `b37abb9`. `syncFromFile` adopts a parsed
non-zero file **wholesale** (`saveToStore(c)`), so a hand-edit that changes or
adds a single line clears every other field in the stored copy — including the
secrets. This is the failure mode Finding 4 set out to prevent, reachable
without an empty file.

```bash
cd /tmp/verify-c && /usr/local/go/bin/go test ./internal/config/ -run TestVerifyC4PartialFileWipesOthers -v
# file: "newapi:\n  url: https://changed.example.com\n"
# url="https://changed.example.com" username="" password=""
# NEW BUG: a partial file wiped the stored password
# NEW BUG: a partial file wiped the stored NapCat WebUI URL
```

Impact: an operator who adds one line to `config.yaml` (or fixes only the URL)
silently loses the new-api password and every NapCat token; the next
`MustLoad()` reports "not configured". Severity: high. Label: pre-existing gap
in the function the fix touched — the fix adds the `isZero` guard but does not
merge a partial file over the stored copy.

### N2 — A group without a sign-in board is now a permanent patrol failure

The "stop masking failures" change in `81b9be1` makes the check-in patrol's
per-group sync error survive into `Status`. But a monitored group with no board
is a **normal** condition (AGENTS.md pitfall: "a group without a board is
normal, not a failure"), and `SyncAllCheckins` still reports it as an error, so
the check-in patrol shows a permanent red failure whenever any monitored group
lacks a board.

```bash
cd /tmp/verify-c && /usr/local/go/bin/go test ./internal/service/ -run TestVerifySchedulerBoardless -v
# SyncAllCheckins with one boardless group -> err=group 2: get_group_signed_list: retcode 1: 无法获取该群组打卡列表
```

Before the change the error was recorded and then erased by the award step's nil
return (the B7 masking bug); the fix correctly stops the masking but does not
distinguish the benign no-board case from a real failure. The new test
(`TestCheckinSyncErrorIsNotMasked`) only exercises the record mechanism, not this
case. Severity: medium (misleading UI, no data loss). Label: behaviour
regression surfaced by the fix.

---

## Summary

**C1–C7: 7 VERIFIED, 0 REFUTED.** All seven original findings are genuinely
closed, confirmed by driving the real code (Go probes under `/tmp/verify-c`) and
a real browser against the built `web/dist`. Two additional bugs were found in
the changed code: **N1** — a partial `config.yaml` still wipes the stored fields
it omits (high); **N2** — a boardless group is now a permanent check-in-patrol
failure (medium).

Note: `internal/store/store.go` and `store_test.go` show uncommitted edits
(WAL + single-connection) dated after the audited commits. They were **not**
authored by this verification and were left untouched.
