# Audit A — service & store correctness

Scope: `internal/service/{service,rules,rules_run}.go`, `internal/store/store.go`.
Repo commit `cfac334`. Read-only on the repo.

Every finding below was reproduced with a throwaway test injected via
`go test -overlay` — the repo files are never modified; the test sources and the
overlay JSON live under
`/root/.hermes/cache/scratch/auditA/` and the test files are mapped onto
`internal/{store,service}/zz_*_audit_test.go` **only for the duration of the
test run**:

```
cd /home/QQbot
/usr/local/go/bin/go test -overlay=/root/.hermes/cache/scratch/auditA/overlay.json \
    -run 'TestAudit' -v -race ./internal/store/... ./internal/service/...
```

Legend: **CONFIRMED** = reproduced with a failing test I ran;
**SUSPECTED** = reasoned but the trigger needs data I could not produce through
the normal call sequence.

---

## A1 — A rule created with `enabled=false` is stored as `enabled=true` (CONFIRMED)

`internal/store/store.go:509-516` (`CreateRule`)

`PromotionRule.Enabled` carries `gorm:"default:true"`. GORM applies a
zero-value-with-default on `Create`, so an explicit `Enabled=false` sent by the
operator is silently replaced by `true`. `UpdateRule` is unaffected because it
uses a `map[string]any` update that includes the `false`.

Impact: the WebUI lets the operator create a rule with 启用 = 停用 (it sends
`enabled:false`); the rule is stored **enabled** and immediately takes part in
the periodic sweep and the on-bind trigger — moving accounts the operator
believed were inert.

Reproduction (fails):
```
go test -overlay=.../overlay.json -run TestAuditCreateRuleEnabledFalse ./internal/store/...
--- FAIL: BUG: rule created with Enabled=false is stored as Enabled=true
```
`TestAuditCreateRuleEnabledFalse` in `store_audit_test.go`.

## A2 — `EnsureMember` deletes every other membership of the group (CONFIRMED)

`internal/service/service.go:284-286` → `internal/store/store.go:196-238`

`EnsureMember` (used by the `group_increase` event and by `>>绑定<<`) is meant to
record **one** member, but it delegates to `UpsertMembers`, which treats its
argument as the group's **authoritative full roster** and deletes every
`member_groups` row for that group not present in the slice
(`store.go:231-236`).

Impact: one `group_increase` event, or one member sending `>>绑定<<`, removes
all other members of that group from `member_groups`. The roster view
(`ListMembers`/`MembersInGroup`, used by `/api/members`) then shows the group as
containing only that member until the next full patrol. `member_groups` is the
record of who is in a group.

Reproduction (fails):
```
go test -overlay=.../overlay.json -run TestAuditEnsureMemberKeepsOtherMemberships ./internal/service/...
--- FAIL: BUG: EnsureMember dropped other memberships: got 1 members, want 3
```
`TestAuditEnsureMemberKeepsOtherMemberships` in `service_audit_test.go`.

## A3 — `>>绑定<<` (and the join event) blanks the stored nickname (CONFIRMED)

`internal/service/service.go:293-296` (`BindByCommand`), `:284-286`

`BindByCommand` calls `EnsureMember(groupID, qq, "")` with an empty nickname, and
`UpsertMembers` unconditionally does `existing.Nickname = m.Nickname`
(`store.go:213`). A member who already has a real nickname (from a roster sync)
has it wiped by their own bind command.

Impact: the operator's user list loses the member's name until the next roster
patrol; the doc comment on `handleGroupIncrease` ("the next roster patrol fills
it in") only holds if a patrol runs before the next sync of that group.

Reproduction (fails):
```
go test -overlay=.../overlay.json -run TestAuditBindCommandBlanksNickname ./internal/service/...
--- FAIL: BUG: >>绑定<< blanked the stored nickname: got "", want "爱丽丝"
```

## A4 — Concurrent `Award` records a reward amount different from the amount paid (CONFIRMED)

`internal/service/service.go:376-386`

`Award` writes the drawn amount with `SetRewardAmount` **before** it claims the
slot with `MarkRewarded`. Two overlapping sweeps (the check-in scheduler
goroutine and the on-demand `POST /api/actions/award`, or two of either) both
read the check-in as unrewarded, both call `SetRewardAmount`, and only then does
one win `MarkRewarded`. The loser sees `claimed=false` and `continue`s, leaving
`reward_quota` holding **its** draw, which was never paid; the winner paid its
own amount. The claim guard prevents double-payment, but not this overwrite.

Impact: `checkins.reward_quota` (shown as `reward_today` in the UI) can differ
from the quota actually granted. The audit trail of what was paid is wrong.

Reproduction (fails, `-race`):
```
go test -overlay=.../overlay.json -run TestAuditConcurrentAward -race ./internal/service/...
--- FAIL: BUG: 34/40 rounds recorded a reward amount different from the paid amount
--- (round 0: paid 107 but recorded reward_quota 137)
```
`TestAuditConcurrentAward` in `concurrency_audit_test.go`. (Double-pay itself was
not observed — `MarkRewarded`'s atomic conditional update holds.)

## A5 — Data race on the shared RNG in `Service.draw` (CONFIRMED)

`internal/service/service.go:88` (`rnd`), `:402-407` (`draw`)

`Service` holds one `*rand.Rand` and `draw` calls `s.rnd.Intn` with no
synchronisation. `Award` runs concurrently from the scheduler goroutine and from
HTTP handlers.

Impact: a genuine Go data race on `math/rand`'s internal source (undefined
behaviour; can return corrupted values or panic). Reproduced with the race
detector.

Reproduction (fails, `-race`):
```
go test -overlay=.../overlay.json -run TestAuditDrawRace -race ./internal/service/...
==================
WARNING: DATA RACE
Read at ... by goroutine ...:
  math/rand.(*rngSource).Uint64()
  qqbot/internal/service.(*Service).draw()
      /home/QQbot/internal/service/service.go:406
--- FAIL: race detected
```
`TestAuditDrawRace` in `service_audit_test.go`.

## A6 — A group-level condition evaluates differently on the sweep and at bind time (CONFIRMED)

`internal/service/rules_run.go:187-193` (sweep) vs `:296-305` (`memberContext`)

For a member who is in more than one monitored group, `rosterByQQ` keeps the
**highest** 群等级 seen across groups (`rules_run.go:328-330`), while
`memberContext` returns the level of the **first** enabled group that contains
the member (groups are ordered by `group_id`, `rules_run.go:291-305`). The two
paths therefore hand a rule different `ctx.Level` for the same member.

Impact: a rule whose condition is 群等级 (e.g. `>= 10`) can promote the member on
the scheduled sweep but not on the bind trigger (or vice-versa), so behaviour
depends on which trigger fired.

Reproduction (fails):
```
go test -overlay=.../overlay.json -run TestAuditLevelConsistencyAcrossPaths ./internal/service/...
--- FAIL: BUG: group level differs between the scheduled sweep (12) and the on-bind path (3)
```

## A7 — Rules fail open (all-zero context) when a group roster is unreadable (CONFIRMED)

`internal/service/rules_run.go:311-335` (`rosterByQQ`), `rules.go:220-257`

`rosterByQQ` `continue`s past a group whose roster cannot be read
(`rules_run.go:319-322`), and a member present in `members` but absent from the
map (unreadable group, or a member who has left every monitored group) gets the
zero-value `MemberContext` — `Level=0, QQLevel=0, JoinDays=0`. `satisfiedBy`
compares those zeros directly.

Impact: a rule with a `<`/`<=` condition (e.g. 入群天数 `<= 1`, a "newcomers"
rule, or 群等级 `< 5`) matches **every** member whose roster could not be read or
who is not in a monitored group, moving accounts it should leave alone. This is
a mass-mis-promotion path.

Reproduction (fails):
```
go test -overlay=.../overlay.json -run TestAuditUnreadableRosterMatchesEveryone ./internal/service/...
--- FAIL: BUG: 2 members promoted by a join_days<=1 rule while their roster was unreadable
```

## A8 — Two rules can move the same account in one sweep (source-group gate is stale) (CONFIRMED)

`internal/service/rules_run.go:162, 177-207` (and `:246-269` for on-bind)

`ApplyRules` caches each account by id (`accounts[b.NewAPIUserID] = u`,
`rules_run.go:184`) and never refreshes it. `Gateway.SetGroup` does not mutate
the passed `*newapi.User` (see `internal/newapi/client.go:196-206` — it only
sends the body), so after rule 1 moves the account out of its source group,
rule 2 still reads the account's **old** `Group` from the cache. If rule 2 also
lists that old group as a source, its source-group gate (`rules.go:196-210`)
passes and it moves the account again in the same sweep.

The existing unit tests miss this because the test fake mutates `u.Group`
(`service_test.go:66-74`); the real client does not.

Impact: the account's final group is whichever rule ran last, not the intended
one; a member can be moved between groups by two rules in a single pass.

Reproduction (fails, using a gateway that mimics the real client):
```
go test -overlay=.../overlay.json -run TestAuditCrossRuleStaleGroup ./internal/service/...
--- FAIL: BUG: one sweep applied 2 rules to the same account; source-group gate was stale (final=vip)
```
`TestAuditCrossRuleStaleGroup` in `extra_audit_test.go`.

## A9 — Concurrent check-in recording hits the UNIQUE constraint and aborts the group's sync (CONFIRMED)

`internal/store/store.go:358-371` (`RecordCheckin`)

`RecordCheckin` does a `First`-then-`Create` without a transaction or an upsert.
Two concurrent writers for the same `(qq, date)` (scheduler sweep + on-demand
`POST /api/actions/checkin`, or two overlapping sweeps) can both miss the
`First`, and the loser's `Create` fails with
`UNIQUE constraint failed: checkins.qq, checkins.date`.

Impact: `SyncCheckins` returns on the first such error (`service.go:210-212`), so
the rest of that group's board is not recorded this round. Data is not
corrupted (the winner wrote the row and the board is a union across polls), but
the sweep reports a failure and the round is partial.

Reproduction (fails intermittently, `-count=3`):
```
go test -overlay=.../overlay.json -run TestAuditConcurrentRecordCheckin -count=3 ./internal/service/...
--- concurrent RecordCheckin: 6/8 returned an error
    (first: constraint failed: UNIQUE constraint failed: checkins.qq, checkins.date (1555))
```
`TestAuditConcurrentRecordCheckin` in `concurrency_audit_test.go`.

## A10 — A configured reward range of 0..0 silently becomes 100..200 (CONFIRMED)

`internal/service/service.go:115-117` (`withDefaults`)

`withDefaults` treats `RewardMin == 0 && RewardMax == 0` as "unset" and replaces
it with the shipped default. There is no way to configure a zero reward, and the
substitution happens on **read** (`Settings`), so even a value written by
`SaveSettings` reads back changed.

Impact: an operator who sets 下限/上限 to 0 (intending no reward) gets a 100–200
reward instead. Low severity, but a real config correctness gap.

Reproduction (fails):
```
go test -overlay=.../overlay.json -run TestAuditZeroRewardRange ./internal/service/...
--- FAIL: BUG: saving reward 0..0 read back as 100..200
```

## A11 — A rule whose conditions blob fails to decode silently matches everyone (SUSPECTED)

`internal/service/rules_run.go:104-129` (`ruleFromStore`)

`ruleFromStore` ignores the `json.Unmarshal` error for `Conditions` and leaves
the slice empty (`rules_run.go:123-128`). An empty condition list is the
deliberate "everyone bound" form, so a rule that originally had conditions and
whose blob is now malformed becomes a match-everything rule.

I reproduced the fail-open by writing a corrupt blob straight to the store
(`TestAuditMalformedConditionsWidenRule`: a `{"not":"an array"}` blob decodes to
0 conditions and the rule then matches a bound member). I mark it **SUSPECTED**
because `CreateRule`/`UpdateRule` always marshal a valid array, so the trigger
requires an out-of-band corruption (manual DB edit, interrupted write, or a
future schema change), not a normal call sequence.

## A12 — The "unfinished rule" guard does not cover conditions-with-no-trigger (SUSPECTED)

`internal/service/rules.go:192-194`

`Matches` guards only `!OnBind && IntervalMinutes <= 0 && len(Conditions) == 0`.
A rule with conditions but no trigger is not treated as unfinished and matches
any member satisfying the conditions. I confirmed `Matches` returns true for
such a rule (`TestAuditNoTriggerWithConditions`), but mark it **SUSPECTED**: it
is unreachable through normal operation, because `Rule.Validate` rejects a rule
with no trigger (`rules.go:129-131`) and both dispatch queries filter on a
trigger (`store.go:547-559`). It is a defence-in-depth gap, not a live bug.

---

## Categories checked and found correct

- **Award idempotency (at most once per `(qq,date)`)**: `MarkRewarded`'s
  conditional `UPDATE ... WHERE rewarded_at = 0` is atomic; a second claim
  returns `RowsAffected == 0`. No double-payment was observed in 40 concurrent
  rounds. (The amount-recorded bug A4 is separate.)
- **Check-in idempotency**: `RecordCheckin` preserves `RewardQuota`/`RewardedAt`
  on re-record and `CountCheckins` counts distinct dates. Existing tests pass.
- **Promotion is a no-op on a second run**: a re-read account already in the
  target group is skipped (`rules_run.go:199-202`, `:262`); existing tests pass.
- **`Rule.Matches` operator table**: all five operators and the nil-`User`
  handling for 余额/请求数 are correct.
- **Join-day arithmetic**: `ContextFromMember` divides the seconds timestamp by
  86400, clamps a missing/zero `JoinTime` to 0, and clamps a future `JoinTime`
  (clock skew) to 0. Verified by test.
- **`UpsertGroups` default**: a newly discovered group is stored with
  `Enabled=true`, matching the documented default.
- **Level fields**: 群等级 (`level`) and QQ 等级 (`qq_level`) are read into the
  right context fields; the flexInt decoder handles NapCat's mixed string/number
  types.
- **Concurrent `SetBinding`**: 8 concurrent writers to one QQ left a single row
  with no error across repeated runs — not reproducible as a defect.

---

## Summary

CONFIRMED: 10   SUSPECTED: 2

Most serious: **A2** (`EnsureMember` deletes the group's other memberships on
every join event and every `>>绑定<<`), **A7** (rules fail open and match
everyone when a roster is unreadable or a member left the group), **A1** (a rule
created disabled is stored enabled and runs), **A8** (two rules move the same
account in one sweep because the cached account is not refreshed), and **A4/A5**
(concurrent sweeps record a reward amount different from the one paid, and race
on the shared RNG).
