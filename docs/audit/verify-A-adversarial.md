# Audit A — adversarial verification of the fixes (independent re-run)

Scope: the A-findings in `docs/audit/A-service-store.md`, fixed in commits
`a0ab232..81b9be1`. Read-only adversarial verification: each fix is proven or
disproven against the original reproduction. No file under `internal/`, `cmd/`
or `web/` was modified in the real checkout.

Method: I extracted a **clean** `git archive 81b9be1` tree (the live working tree
carries an unrelated uncommitted SQLite-pragma change to `internal/store/store.go`
that I did not make and did not rely on) and dropped throwaway test files into it:

```
mkdir -p /root/.hermes/cache/scratch/advA/repo
cd /home/QQbot && git archive 81b9be1 | tar -x -C /root/.hermes/cache/scratch/advA/repo
cp advA/store_adv_test.go   repo/internal/store/zz_adv_test.go
cp advA/service_adv_test.go repo/internal/service/zz_adv_test.go
cd repo && export PATH=$PATH:/usr/local/go/bin
go test -run 'TestAdv' -v -race ./internal/store/... ./internal/service/...
```

Legend: **VERIFIED** = the fix closes the original reproduction, proven by a test
I ran; **REFUTED** = the fix does not close it; **PARTIAL** = the stated symptom
is fixed but the underlying class of bug survives for a sibling input.

| # | Claim | Verdict |
|---|-------|---------|
| A1 | disabled rule stays disabled (fresh handle) | **VERIFIED** |
| A2 | EnsureMember is additive | **VERIFIED** |
| A3 | blank nickname preserved | **VERIFIED** |
| A4 | recorded reward == paid reward under concurrency | **VERIFIED** |
| A5 | no data race on the shared RNG | **VERIFIED** |
| A6 | group-level condition consistent across paths | **PARTIAL** |
| A7 | rules fail closed when roster unreadable | **VERIFIED** |
| A8 | two rules cannot move the same account twice | **VERIFIED** |
| A9 | concurrent RecordCheckin does not trip UNIQUE | **VERIFIED** |
| A10 | 0..0 reward range survives; missing still defaults | **VERIFIED** |
| A11 | corrupt conditions blob does not widen the rule | **PARTIAL** |

Result: **9 VERIFIED, 0 REFUTED, 2 PARTIAL.**

---

## A1 — VERIFIED

Command: `go test -run 'TestAdvA1' -v ./internal/store/...`
Observed: `--- PASS: TestAdvA1DisabledRuleFreshHandle` / `--- PASS: TestAdvA1MonitoredGroupDefault`

`PromotionRule.Enabled` no longer carries `gorm:"default:true"`. A rule created
`Enabled:false` through the store is returned false, is read back false from a
**fresh** `store.Open` handle, and stays out of both `ScheduledRules` and
`OnBindRules`. No over-correction: an `Enabled:true` rule still stores true and
dispatches. The only other `default:` tag in the whole package is
`MonitoredGroup.Enabled` (grep: `internal/store/store.go:24`), reached only
through `UpsertGroups` — a newly discovered group is still enabled by default and
`SetGroupEnabled(false)` (a map update) still takes effect, so removing the rule
tag did not strand it.

## A2 — VERIFIED

Command: `go test -run 'TestAdvA2' -v ./internal/service/...`
Observed: `--- PASS: TestAdvA2EnsureMemberAdditive`

Seeded 3 members in group 1 (and a second membership for member 2 in group 2),
then `svc.EnsureMember(1, 4, "")`: group 1 still has 4 memberships and group 2 is
untouched. `service.EnsureMember` now calls the additive `store.EnsureMember`,
which upserts only the single `(qq)` member row and the single `(groupID, qq)`
membership row; it no longer routes through `UpsertMembers`'s roster
reconciliation. `UpsertMembers` is still the only path used by `SyncMembers`.

## A3 — VERIFIED

Command: `go test -run 'TestAdvA3' -v ./internal/service/...`
Observed: `--- PASS: TestAdvA3NicknamePreserved`

`EnsureMember` overwrites the nickname only when `strings.TrimSpace(nickname)` is
non-empty. Adversarial inputs pass: `""` and whitespace-only both leave the stored
name intact, a real nickname is still recorded, and a brand-new member with no
nickname is still created.

## A4 — VERIFIED

Command: `go test -run 'TestAdvA4' -race -v ./internal/service/...`
Observed: `--- PASS: TestAdvA4ConcurrentAwardRecordsPaid (60 rounds x 12 goroutines)` / `--- PASS: TestAdvA4FailureReleaseRetry`

`Award` draws once and calls `store.ClaimReward(qq, date, amount, now)`, a single
`UPDATE ... WHERE rewarded_at = 0` that sets `rewarded_at` and `reward_quota`
atomically, so the recorded amount can only be the winner's draw — the one paid.
60 rounds of 12 concurrent `Award` calls (range 100–99999, the gateway fake
configured `mutateUser=false` so it does **not** mutate the caller's struct) each
produced exactly one `AddQuota`, and `reward_quota` equalled the paid value every
round. The failure path is intact: a gateway error releases the claim and the
retry's recorded amount equals what it then pays.

## A5 — VERIFIED

Command: `go test -run 'TestAdvA5' -race -v ./internal/service/...`
Observed: `--- PASS: TestAdvA5DrawRace`

64 goroutines × 500 `draw` calls under `-race` report no data race and no
out-of-range value. `Service.draw` holds `rndMu` around `s.rnd.Intn`. The whole
`go test -race ./internal/service/` is also clean.

## A6 — PARTIAL

Command: `go test -run 'TestAdvA6' -v ./internal/service/...`
Observed:
```
A6: sweep Level=12 QQLevel=5 JoinDays=2 | bind Level=12 QQLevel=40 JoinDays=30
--- FAIL: TestAdvA6AllQQFieldsConsistent (qq_level differs: sweep=5 bind=40; join_days differs: sweep=2 bind=30)
```

The **named** symptom — a 群等级 condition evaluating differently at bind time vs
sweep — is fixed: `memberContext` now takes the max Level across monitored groups
(12 == 12), matching the sweep. But the fix only reconciled **Level**:

- bind path (`memberContext`) maxes Level, QQLevel **and** JoinDays;
- sweep (`rosterByQQ`) maxes only Level and lets QQLevel/JoinDays be
  last-write-wins across groups.

So for a member in two monitored groups a rule conditioned on 入群天数 or
QQ 等级 still evaluates differently on the two paths — exactly the class the
finding names, for sibling fields. The fix's own comment ("a rule cannot evaluate
differently depending on whether the bind trigger or the patrol fired it") is not
achieved.

## A7 — VERIFIED

Command: `go test -run 'TestAdvA7' -v ./internal/service/...`
Observed: `--- PASS: TestAdvA7FailClosed`

`MemberContext.HasQQData` is set only by `ContextFromMember`; `satisfiedBy`
returns false for `group_level`, `qq_level` and `join_days` when it is unset. A
`join_days<=1` rule does NOT match `HasQQData=false`, but DOES match a genuine
`HasQQData=true, JoinDays=0`. `群等级<5` and `QQ等级<5` fail closed the same way.
`checkin_days` is **not** gated (our own store) and still matches with
`HasQQData=false`.

## A8 — VERIFIED

Command: `go test -run 'TestAdvA8' -v ./internal/service/...`
Observed: `--- PASS: TestAdvA8StaleGroup`

With the gateway fake configured `mutateUser=false` (the real client does not
mutate the caller's struct), two on-bind rules sharing source group `default`
move the account exactly once; the final group is the first rule's target. After
a successful `SetGroup`, `ApplyRules` (and `ApplyOnBindRules`) set
`u.Group = rule.TargetGroup` on the cached user, so the second rule's source gate
fails. A second sweep is still a no-op (idempotence preserved).

## A9 — VERIFIED

Command: `go test -run 'TestAdvA9' -v ./internal/store/...`
Observed: `--- PASS: TestAdvA9ConcurrentRecordCheckin`

32 concurrent `RecordCheckin` calls for one `(qq, date)` produce 0 errors and
exactly 1 row. `RecordCheckin` is a single `clause.OnConflict` upsert on
`(qq, date)` updating only `signed_at`, `rank`, `group_id`, so it also cannot
clobber a `reward_quota` already decided (verified: a preset 150 survives a
re-record, while `signed_at` does update).

## A10 — VERIFIED

Command: `go test -run 'TestAdvA10' -v ./internal/service/...`
Observed: `--- PASS: TestAdvA10ZeroRewardRange`

`withDefaults` now falls back only on a **negative** range, so a saved 0..0 reads
back 0..0 and `draw(0..0)` pays 0. A genuinely missing range (fresh DB, no
settings row) still defaults to 100..200, so the original default behaviour is
preserved.

## A11 — PARTIAL

Command: `go test -run 'TestAdvA11' -v ./internal/service/...`
Observed:
```
--- PASS: TestAdvA11CorruptConditions   (blob `{"not":"an array"}`)
--- FAIL: TestAdvA11NullConditions      (blob `null` widened to an unconditional rule and matched a member)
```

`ruleFromStore` records `Rule.DecodeError` when the conditions blob fails to
unmarshal, and `Matches` returns false for such a rule, so the reported
`{"not":"an array"}` case is closed. The guard is narrower than the finding: a
literal JSON `null` **unmarshals without error** into a nil slice, so
`DecodeError` stays empty, the `Conditions == nil` fallback converts it to
`[]Condition{}`, and the rule becomes an unconditional "match everyone bound"
rule again. A corrupted rule that originally had `group_level >= 99` matched a
member. The class the finding names — "a conditions blob that no longer carries
the original conditions silently widens the rule" — is only partly closed.
