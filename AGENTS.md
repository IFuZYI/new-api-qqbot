# AGENTS.md — qqbot

A QQ group monitor that links QQ group members to new-api accounts. It mirrors
group rosters and the daily sign-in board, binds each member to the new-api
account registered under their QQ email (`qq@qq.com`), promotes accounts out of
a source group into a target group, and pays a daily check-in reward. It reads
QQ and does not speak in groups, except to act on the `>>绑定<<` command.

`reference/` holds two upstream checkouts (`NapCatQQ`, `new-api`) used as
documentation for the protocols this service drives. They are **not** part of
the build and must not be edited; read them and cite them. `reference/new-api/`
has its own binding `AGENTS.md`.

## Layout

```
cmd/qqbot/          entry point, flags, lazy clients
internal/store/     SQLite schema, migrations, queries
internal/service/   business logic (sync, bind, promote, award) against interfaces
internal/newapi/    new-api admin API client
internal/napcat/    OneBot HTTP client + NapCat WebUI bridge
internal/events/    OneBot WebSocket listener (binding command)
internal/api/       HTTP API + static file serving
internal/config/    connection settings stored in the database
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
Auth state lives in the `admins` table; the session cookie is HMAC-signed with a
key generated on first use and stored in settings. Only `/api/auth/state`,
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
- **Bindings are owned by this service.** new-api has no admin endpoint that
  writes a user's email (see `docs/adr/0002-binding-owned-locally.md`), so the
  mapping lives here and new-api is read-only for email.
- **Auto-binding requires an exact, unique match.** `FindUserByEmail` narrows the
  gateway's LIKE search to a case-insensitive exact match; ambiguous matches are
  reported and left unbound rather than guessed at.
- **A check-in is idempotent per `(qq, date)`.** The sign-in board is today-only
  and polled repeatedly, so re-recording must never re-randomise the reward or
  re-award it. The reward amount is persisted *before* the gateway call, and the
  claim is released only when that call fails.
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
- **`get_group_signed_list` errors when a group has no sign-in board**, and
  returns only today's page. Treat it as a union across polls; a group without a
  board is normal, not a failure.
- **NapCat's OneBot config can be rewritten live** over the WebUI
  (`/api/OB11Config/SetConfig` → save + reload), so enabling an adapter needs no
  restart — do not claim otherwise.
- Never write to `reference/`; it is upstream material that may be refreshed.

## Verifying against the real systems

Config is stored in the database and edited in the WebUI (连接配置). For a local
check, the pieces to confirm by hand are: `POST /api/groups/refresh` (reads real
groups), `POST /api/actions/sync`, `.../bind`, `.../promote`, `.../award`.
Promotion and awarding spend real state on the gateway — exercise them on a
throwaway account and revert (group back to the source group, quota subtracted)
when done.
