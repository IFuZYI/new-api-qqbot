# Audit A — adversarial verification of the fixes

Scope: the A-findings in `docs/audit/A-service-store.md`, fixed in commits
`a0ab232..81b9be1`. This is a **read-only** adversarial verification: each fix is
proven or disproven against the original reproduction. No file under `internal/`,
`cmd/` or `web/` is modified.

Method: throwaway test files injected with `go test -overlay` (the same technique
as the original audit). The test sources and the overlay JSON live under
`/root/.hermes/cache/scratch/verifyA/` and are mapped onto
`internal/{store,service}/zz_verify_test.go` **only for the duration of the test
run**:

```
cd /home/QQbot
export PATH=$PATH:/usr/local/go/bin
go test -overlay=/root/.hermes/cache/scratch/verifyA/overlay.json \
    -run 'TestVerify' -v -race ./internal/store/... ./internal/service/...
```

The whole repo suite (`go test -race ./internal/...`) and `go vet ./internal/...`
pass; no source file was touched.

**Isolation note.** Other agent sessions were running in this same checkout
during verification and modified `internal/store/store.go` (SQLite busy-timeout /
WAL pragmas) in the shared working tree — a change that is **not** part of
`81b9be1` and that I did not make. To anchor the verdicts to the commit under
review, every result below was reproduced twice: once via the overlay on the live
working tree, and once inside a clean `git archive 81b9be1` extraction (test files
copied directly into `internal/{store,service}/`). Both runs agree exactly. The
overlay command above is the working-tree form; the clean-tree form is:

```
git archive 81b9be1 | tar -x -C <tmp>
cp verifyA/store_verify_test.go  <tmp>/internal/store/zz_verify_test.go
cp verifyA/service_verify_test.go <tmp>/internal/service/zz_verify_test.go
cd <tmp> && go test -run 'TestVerify' -v -race ./internal/store/... ./internal/service/...
```

Legend: **VERIFIED** = the fix closes the original reproduction, proven by a test
I ran; **REFUTED** = the fix does not close it; **PARTIAL** = the stated symptom
is fixed but the underlying class of bug survives for a sibling input.

| # | Claim | Verdict |
|---|-------|---------|
| A1 | disabled rule stays disabled | **VERIFIED** |
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

Command:
```
go test -overlay=.../overlay.json -run TestVerifyA1 -v ./internal/store/...
```
Observed:
```
--- PASS: TestVerifyA1DisabledRuleFreshHandle
--- PASS: TestVerifyA1MonitoredGroupDefaultStillWorks
```

`store.PromotionRule.Enabled` no longer carries `gorm:"default:true"`. A rule
created with `Enabled:false` is returned false by `CreateRule`, and a **fresh**
`store.Open` handle reads it back false and keeps it out of both `ScheduledRules`
and `OnBindRules`. No over-correction: an `Enabled:true` rule still stores true
and dispatches. The only other `default:` tag is `MonitoredGroup.Enabled`, which
is reached only through `UpsertGroups` (never an explicit false) — the second
test confirms a newly discovered group is still enabled by default and that
`SetGroupEnabled(false)` (a map update) still works, so removing the rule tag did
not strand it.

## A2 — VERIFIED

Command:
```
go test -overlay=.../overlay.json -run TestVerifyA2 -v ./internal/service/...
```
Observed: `--- PASS: TestVerifyA2EnsureMemberAdditive`

Seed 3 members in group 1 (plus a second membership for member 2 in group 2),
then `svc.EnsureMember(1, 4, "")`: group 1 still has 4 memberships and group 2 is
untouched. `service.EnsureMember` now calls the additive `store.EnsureMember`,
which only upserts the single `(qq)` member row and the single `(groupID, qq)`
membership row; it no longer routes through `UpsertMembers`'s roster
reconciliation (the `Delete` of unlisted memberships). The full-roster path is
still the only caller of `UpsertMembers` (`SyncMembers`), which is correct.

## A3 — VERIFIED

Command:
```
go test -overlay=.../overlay.json -run TestVerifyA3 -v ./internal/service/...
```
Observed: `--- PASS: TestVerifyA3BlankNicknamePreserved`

`EnsureMember` overwrites the nickname only when `strings.TrimSpace(nickname)`
is non-empty, so `>>绑定<<` and the join event (both pass `""`) cannot blank a
name a roster sync stored. Adversarial inputs pass too: a whitespace-only
nickname is ignored, a real nickname is still recorded, and a brand-new member
with no nickname is still created.

## A4 — VERIFIED

Command:
```
go test -overlay=.../overlay.json -run 'TestVerifyA4' -v -race ./internal/service/...
```
Observed:
```
--- PASS: TestVerifyA4ConcurrentAwardRecordsPaid (40 rounds x 8 goroutines)
--- PASS: TestVerifyA4FailureReleaseRetry
```

`Award` now draws the amount and calls `store.ClaimReward(qq, date, amount, now)`
once; that single `UPDATE ... WHERE rewarded_at = 0` sets `rewarded_at` and
`reward_quota` atomically, so the recorded amount can only ever be the winner's
draw — the one that is then paid. 40 rounds of 8 concurrent `Award` calls (range
100–9999, the gateway fake configured **not** to mutate the caller's struct) each
produced exactly one `AddQuota`, and the persisted `reward_quota` equalled the
paid value every round. The failure path is intact: a gateway error releases the
claim, and the retry's recorded amount equals what it then pays.

## A5 — VERIFIED

Command:
```
go test -overlay=.../overlay.json -run TestVerifyA5 -v -race ./internal/service/...
```
Observed: `--- PASS: TestVerifyA5DrawRace`

32 goroutines × 200 `draw` calls under `-race` report no data race. `Service.draw`
holds `rndMu` around `s.rnd.Intn`. (`go test -race ./internal/...` is also clean.)

## A6 — PARTIAL

Command:
```
go test -overlay=.../overlay.json -run 'TestVerifyA6' -v ./internal/service/...
```
Observed:
```
--- PASS: TestVerifyA6LevelConsistency        (sweep level=12 bind level=12)
--- PASS: TestVerifyA6RuleFiresSameOnBothPaths
--- FAIL: TestVerifyA6AllQQFieldsConsistent   (qq_level differs: sweep=5 bind=40; join_days differs: sweep=2 bind=30)
--- FAIL: TestVerifyA6JoinDaysRuleDiverges    (sweep promoted=0, bind promoted=1)
```

The stated symptom — a **群等级** condition evaluating differently at bind time
vs sweep — is fixed. `memberContext` now takes the max level across monitored
groups, matching the sweep's `rosterByQQ`, and a `level>=10` rule now fires on
both paths for the same member.

But the fix only made the **Level** field consistent. The bind path
(`rules_run.go` `memberContext`) now maxes Level, QQLevel **and** JoinDays, while
the sweep (`rules_run.go` `rosterByQQ`) still only maxes Level and lets QQLevel /
JoinDays be **last-write-wins** across groups:

```go
// rosterByQQ (sweep) — only Level is reconciled:
if prev, ok := out[m.QQ]; ok && prev.Level > c.Level { c.Level = prev.Level }
out[m.QQ] = c   // QQLevel and JoinDays come from whichever group was read last
```

So for a member in two monitored groups, a rule conditioned on 入群天数 or
QQ 等级 can still evaluate differently on the two paths. The end-to-end test
proves it: a `join_days >= 10` rule does not promote on the sweep (last group
reports 2 days) but does promote at bind time (max of 30 days). The fix closed
the named 群等级 case but not the general "same member, same rule, two answers"
class the finding is about.

## A7 — VERIFIED

Command:
```
go test -overlay=.../overlay.json -run TestVerifyA7 -v ./internal/service/...
```
Observed: `--- PASS: TestVerifyA7FailClosed`

`MemberContext.HasQQData` is set only by `ContextFromMember` (a real roster
read), and `Condition.satisfiedBy` returns false for `group_level`, `qq_level`
and `join_days` when it is unset. A `join_days<=1` rule no longer matches a
member with `HasQQData=false`, but still matches a genuine `JoinDays=0` with
data. `群等级 < 5` fails closed the same way. `checkin_days` is **not** gated
(our own store), and still matches with `HasQQData=false`. End-to-end through
`ApplyRules`, an unreadable roster promotes nobody.

## A8 — VERIFIED

Command:
```
go test -overlay=.../overlay.json -run TestVerifyA8 -v ./internal/service/...
```
Observed: `--- PASS: TestVerifyA8StaleGroup`

With the gateway fake configured `mutateUser=false` (the real client does not
mutate the caller's struct), two on-bind rules that share source group `default`
move the account exactly once; the final group is the first rule's target. After
a successful `SetGroup`, `ApplyRules` (and `ApplyOnBindRules`) set
`u.Group = rule.TargetGroup` on the cached user, so the second rule's source gate
fails. A second sweep is still a no-op (idempotence preserved).

## A9 — VERIFIED

Command:
```
go test -overlay=.../overlay.json -run TestVerifyA9 -v ./internal/store/...
```
Observed:
```
--- PASS: TestVerifyA9ConcurrentRecordCheckin
    A9: 24 concurrent writers, 0 errors
```

24 concurrent `RecordCheckin` calls for one `(qq, date)` produce 0 errors and
exactly 1 row. `RecordCheckin` is now a single `clause.OnConflict` upsert on
`(qq, date)` that updates only `signed_at`, `rank`, `group_id` — so it also
cannot clobber a `reward_quota` already decided.

## A10 — VERIFIED

Command:
```
go test -overlay=.../overlay.json -run TestVerifyA10 -v ./internal/service/...
```
Observed: `--- PASS: TestVerifyA10ZeroRewardRange`

`withDefaults` now falls back only on a **negative** range, so a saved 0..0 reads
back 0..0 and `draw(0..0)` pays 0. A genuinely missing range (fresh DB, no
settings row) still defaults to 100..200, so the original default behaviour is
preserved.

## A11 — PARTIAL

Command:
```
go test -overlay=.../overlay.json -run 'TestVerifyA11' -v ./internal/service/...
```
Observed:
```
--- PASS: TestVerifyA11CorruptConditions    (blob `{"not":"an array"}`)
--- FAIL: TestVerifyA11NullConditions       (blob `null` widened to an unconditional rule and matched a member)
```

`ruleFromStore` now records `Rule.DecodeError` when the conditions blob fails to
unmarshal, and `Matches` returns false for such a rule — so the reported
`{"not":"an array"}` case is closed (the corrupt rule neither matches nor
promotes on the on-bind sweep).

The guard is narrower than the finding, though. It only covers a blob that
**fails to unmarshal**. A literal JSON `null` unmarshals into a nil slice with no
error, so `DecodeError` stays empty, the `Conditions == nil` fallback converts it
to `[]Condition{}`, and the rule becomes an unconditional "match everyone bound"
rule again. `TestVerifyA11NullConditions` shows the corrupted rule (originally
`group_level >= 99`) matching a member and, on the on-bind path, being eligible
to move them. The finding's class — "a conditions blob that no longer carries the
original conditions silently widens the rule" — is therefore only partly closed:
non-decoding garbage is contained, but a valid-but-empty (`null`) value is not.
