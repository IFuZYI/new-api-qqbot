# AGENTS.md — qqbot

A QQ group monitor that links QQ group members to new-api accounts. It mirrors
group rosters and the daily sign-in board, binds each member to the new-api
account registered under their QQ email (`qq@qq.com`), promotes accounts out of
a source group into a target group, and pays a daily check-in reward. It reads
QQ and does not speak in groups, except to act on the operator's configured bind
and check-in commands.

Binding and check-ins are each several independent paths (a command, a join
event, a patrol; the sign-in board, a message), every one of them switchable and
configured from the WebUI — see `docs/adr/0009-bind-and-checkin-paths-are-modular.md`.

`reference/` holds two upstream checkouts (`NapCatQQ`, `new-api`) used as
documentation for the protocols this service drives. They are **not** part of
the build and must not be edited; read them and cite them. `reference/new-api/`
has its own binding `AGENTS.md`.

## Layout

```
cmd/qqbot/          entry point, flags, lazy clients
internal/store/     SQLite schema, migrations, queries
internal/service/   business logic (sync, bind, promote, award) against interfaces
internal/command/   command-pattern parsing and matching (literal + regex)
internal/newapi/    new-api admin API client
internal/napcat/    OneBot HTTP client + NapCat WebUI bridge
internal/events/    OneBot WebSocket listener (commands, joins, message check-ins)
internal/scheduler/ periodic sweeps, one loop per path
internal/api/       HTTP API + static file serving
internal/config/    connection settings stored in the database, plus the env file
web/src/features/   one module per feature area of the panel
web/                React + Vite + Tailwind WebUI
docs/adr/           decision records; docs/design-notes.md has the verified facts
```

## Build, test, run

```bash
go build ./...                      # Go 1.27 (binary at /usr/local/go/bin/go)
go test ./internal/...              # all tests; no network or live services needed
go vet ./... && gofmt -l internal/ cmd/

cd web && npm install && npm run build   # typechecks (tsc) then builds to web/dist

go run ./cmd/qqbot -addr :4096 -data data -web web/dist
```

The WebUI is served by the Go binary from `-web` (default `web/dist`). If that
directory is missing, the binary serves an explanatory page and the API still
works — build the frontend only when you need the UI.

The panel requires a login: on first run the UI asks for an admin username and
password (minimum 8 characters) and that same form becomes the login afterwards.
The admin can also be provisioned from the environment — `.env` beside the code
(or `QQBOT_ENV_FILE`), or the process environment, via
`QQBOT_ADMIN_USERNAME` / `QQBOT_ADMIN_PASSWORD` — read at startup before the
server accepts requests, so a fresh instance is never briefly open. The same
file sets the listen port (`QQBOT_PORT`, a bare port or a full address; an
explicit `-addr` flag wins over it, which wins over the default `:4096`). Auth
state lives in the `admins` table; the session cookie is HMAC-signed with a key
generated on first use and stored in settings. Only `/api/auth/state`,
`/api/auth/setup`, `/api/auth/login` and `/api/auth/logout` are reachable
without a session — add new endpoints through `r.requireAuth(...)`.

Tests are pure Go and use fakes for both external systems, so they run offline.
Anything that talks to the real gateway or NapCat must be verified by hand (see
below) and never as part of `go test`.

## Conventions

- Conventional Commits with a scope, lowercase: `feat(service): …`, `fix(store): …`.
- Business logic lives in `internal/service` and depends on the small interfaces
  `Gateway` and `QQ`, never on the concrete clients. New external behaviour goes
  behind one of those interfaces so it stays testable.
- Each package's doc comment states what it owns. Comments explain *why* a rule
  exists (especially idempotency and ordering), not what the line does.
- UI text is Simplified Chinese, matching the operator-facing docs.

## Invariants — do not break these

- **A member is keyed by QQ and never deleted.** Group membership lives in
  `member_groups`, so a member who leaves one group keeps their binding and
  check-in history. `UpsertMembers` reconciles memberships, not member rows.
- **A newly discovered group is stored disabled.** Monitoring a group means
  recording its members and paying them a reward, and the bot can be invited
  into unrelated groups, so it takes an explicit choice. `MonitoredGroup.Enabled`
  therefore carries no `gorm:"default:true"` (a zero-value-with-default would
  re-enable it on every create), and `UpsertGroups` preserves the operator's
  choice across a refresh — only the metadata is updated.
- **Bindings are owned by this service.** new-api has no admin endpoint that
  writes a user's email (see `docs/adr/0002-binding-owned-locally.md`), so the
  mapping lives here and new-api is read-only for email.
- **Auto-binding requires an exact, unique match.** `FindUserByEmail` narrows the
  gateway's LIKE search to a case-insensitive exact match; ambiguous matches are
  reported and left unbound rather than guessed at.
- **Every binding path goes through `Service.bindQQ`, and none of them may touch
  a member who already has a binding.** The patrol, the join event and the
  command differ only in their trigger and in what "no account yet" means. This
  is what makes all three switches safe to turn on: enabling one cannot re-point
  a binding the operator made by hand.
- **A check-in is idempotent per `(qq, date)`.** The sign-in board is today-only
  and polled repeatedly, so re-recording must never re-randomise the reward or
  re-award it. The reward amount is persisted *before* the gateway call, and the
  claim is released only when that call fails. A message-driven check-in writes
  the same row through `RecordMessageCheckin`, which only ever inserts: a day
  already recorded (by the board or by an earlier message) is left exactly as it
  is, and the board upgrades a message-sourced row because it knows the rank and
  the sign-in time. Neither path touches `reward_quota` / `rewarded_at`, so
  "any message counts as a check-in" cannot turn into multiple rewards a day.
- **An empty check-in command list means "any message"; a non-empty one means
  "only these".** The master switch enables the path, the list decides which
  messages qualify. Reading the switch alone as "any message" would silently
  override a configured command list and check in everyone who speaks.
- **A switched-off path makes no request at all.** Every sweep is gated on its
  own switch, and the expensive ones (roster reads, sign-in boards) are gated
  again at the point of the call: `SyncAllCheckins` is skipped when the board
  patrol is off, and `rosterByQQ` / `memberContext` are skipped when no rule in
  the batch has a QQ-side condition (`Rule.NeedsQQData`). Turning a feature off
  must remove its traffic, not just its effects.
- **Rewards are paid by the check-in loop, which therefore runs while either
  check-in path is on.** The board read (one QQ request per group) and the award
  step are separable: an operator running message check-ins with the board
  patrol off must still be paid. Gating the whole loop on the board switch
  silently stopped every reward — do not re-merge the two.
- **An invalid command pattern is refused at save time** (`command.Parse`), with
  a 400 naming the problem. A pattern that can never match is the hardest failure
  of this feature to notice, so it must not be storable.
- **Group conversion is rules-driven, with one decision point.** Each rule
  picks its trigger (on bind and/or a patrol interval), source groups, target
  group and optional conditions; a rule with no trigger and no conditions is
  unfinished and matches nothing, so it cannot move a whole group. Do not add a
  second, implicit conversion path: the old blanket `Promote` sweep ran
  alongside the rules and moved accounts behind the operator's back.
- **Conversion must be a no-op on a second run**, and only touches accounts
  sitting in the rule's source groups. Re-read the account by id before writing:
  new-api's `PUT /api/user/` replaces the fields it accepts, so a sparse object
  blanks `display_name`.
- **Polling is the source of truth.** The event listener only shortens reaction
  time; nothing may depend on it (see `docs/adr/0003-polling-authoritative-events-optional.md`).
- **The user directory is a union of both sides, keyed on QQ *and* new-api
  account.** The table exists to answer "谁没进群": a new-api account no member
  has joined with, and a QQ member with no account, both appear. A bound account
  must never be dropped just because no `members` row carries it (the manual
  bind path accepts any QQ), so the account loop skips an account only when a
  member row already showed it. `newapi_users` is a *cache* refreshed by
  `POST /api/users/sync`; a full replace is deliberate, so it must only be
  written after a complete read — a partial fetch would delete real accounts.
- **A group change re-reads the account by id first.** Same reason as conversion:
  new-api's `PUT /api/user/` replaces the fields it accepts, so a sparse object
  blanks `display_name`. Writing the group an account already has is a no-op
  (no gateway call). A batch reports each account's outcome separately, because
  one role-protected account must not block the rest of the selection.

## Pitfalls

- **The gateway sits behind Cloudflare and rejects Go's default User-Agent.**
  Both clients send an identifying `User-Agent`; dropping it yields HTTP 403.
- **An admin cannot manage an account at the same or a higher role.** Promoting
  or paying the account this service itself logs in as fails with
  "No permission to access users of same or higher level". Verify against a
  common user (role 1), not the admin account.
- **A group name must exist on the instance** (in both the usable-groups and
  group-ratio settings) before it can be assigned. Check `/api/user/groups`.
- **The gateway caps active login sessions (50 by default) and answers 409
  AUTH_SESSION_LIMIT.** Logging in per request exhausts that under any real
  sweep, so the client is cached and only re-authenticates when the config
  changes or a request is rejected. Sessions live 30 days, so repeated logins
  also accumulate: if the cap is hit, an existing session must be revoked
  (`POST /api/user/sessions/revoke-others`) or they must expire.
- **NapCat's WebUI credential is signed with a per-process secret** unless
  `NAPCAT_WEBUI_JWT_SECRET_KEY` is set, so it dies with the NapCat process and
  must be re-obtained (tolerating the login rate limit) after a restart.
- **A NapCat WebUI address is normalised to its origin before use.** Operators
  paste the address in the browser bar, which is the panel at `.../webui/`, but
  NapCat's API lives at the root (`/api/...`); a carried path makes every call
  hit the SPA's HTML instead of JSON. `napcat.NormalizeWebUIURL` strips it, and
  the connection page's auto-configure is what wires the OneBot adapters up.
- **`localhost` is never used for the OneBot URLs; it becomes 127.0.0.1.** The
  adapters bind IPv4 loopback, but `localhost` usually resolves to `::1` first,
  and a 127.0.0.1 listener is unreachable through `::1` — so a stored
  `http://localhost:3002` fails with "connection refused" while everything is
  actually running. `napcat.WebUIHost` maps the name; the URLs it returns are
  what gets stored.
- **`get_group_signed_list` errors when a group has no sign-in board**, and
  returns only today's page. Treat it as a union across polls; a group without a
  board is normal, not a failure — the client reports it as
  `napcat.ErrNoSigninBoard` so a sweep can ignore it without hiding real errors.
  A board's `rank` is fractional (NapCat computes `(rank-1)/2+1`), so it is
  decoded as a float and rounded for storage; an int field loses the whole page.
- **The user search is paginated.** `FindUserByEmail` pages through
  `/api/user/search` because a real exact match can sort past the first page, and
  reading only page 1 reports the member as unbound forever.
- **SQLite runs on a single connection.** The member and check-in patrols write
  from separate goroutines; with two connections they can each hold a read lock
  and then both try to upgrade, which deadlocks and `busy_timeout` does not
  cover. Serialising at the pool (plus WAL) removes that path.
- **NapCat's OneBot config can be rewritten live** over the WebUI
  (`/api/OB11Config/SetConfig` → save + reload), so enabling an adapter needs no
  restart — do not claim otherwise.
- Never write to `reference/`; it is upstream material that may be refreshed.
- **The admin credential in the environment is authoritative, but blank means
  "leave it alone".** `QQBOT_ADMIN_PASSWORD` set to a value creates the admin or
  resets its password at startup (the recovery path when it is lost); unset or
  whitespace-only leaves the stored admin untouched, so an empty line in a
  provisioning template cannot wipe the panel's only gate. Re-applying the same
  value is a no-op (it must not rewrite the hash, or every restart would revoke
  every session); a *changed* password bumps the session generation like any
  other password change. A configured username that differs from an existing one
  *adds* an account rather than replacing it, so a typo cannot lock the operator
  out.

## Verifying against the real systems

Config is stored in the database and edited in the WebUI (连接配置). For a local
check, the pieces to confirm by hand are: `POST /api/groups/refresh` (reads real
groups), `POST /api/actions/sync`, `.../bind`, `.../promote`, `.../award`, and
`.../checkin-member` (records a message-driven check-in for one member, which is
how the message path is verified without waiting for someone to chat).
Promotion and awarding spend real state on the gateway — exercise them on a
throwaway account and revert (group back to the source group, quota subtracted)
when done.

Two of the new paths are driven by push events, so verifying them needs an event
stream. The service re-reads the event URL on every reconnect, so a stand-in
WebSocket server that replays NapCat's event JSON (a `group_increase` notice, a
message with `raw_message`) exercises the listener without a live QQ session —
which matters, because NapCat's QQ login expiring is common and it takes the
OneBot adapters down with it. `GET /api/status` then shows whether each patrol
ran, and the check-in's `source` column says which path recorded it.
