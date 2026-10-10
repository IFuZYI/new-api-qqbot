# AGENTS.md — /home/QQbot reference workspace

This directory is **not an application**. It is a workspace holding two upstream
reference repositories that are read to design, compare, and implement features
elsewhere:

- `reference/NapCatQQ` — modern QQ bot protocol framework on NTQQ (TypeScript, pnpm monorepo).
- `reference/new-api` — AI API gateway/proxy aggregating 40+ providers (Go backend + React frontend).

Each repo is its own git clone with its own instruction file (`reference/new-api/AGENTS.md`,
`reference/new-api/web/AGENTS.md`). **Read the nested `AGENTS.md` before editing files inside
that repo** — the rules there (billing read-gate, DB compatibility, component reuse) are
binding and are not repeated here. Treat both clones as reference material: prefer reading
and citing them over modifying them, and never `git pull`/commit in them without being asked.

## Layout

```
reference/NapCatQQ/   packages/* (pnpm workspace), eslint.config.js, tsconfig.base.json
reference/new-api/    main.go, model/ controller/ service/ relay/ relaykit/ tokenkit/ web/
```

## NapCatQQ (pnpm monorepo, Node >= 18, CI uses Node 20 + pnpm 9)

Run from `reference/NapCatQQ/`:

```bash
pnpm i                          # install workspace deps
pnpm run typecheck              # pnpm -r --if-present run typecheck
pnpm test                       # vitest in packages/napcat-test
pnpm run test:ui                # vitest --ui
pnpm run lint                   # eslint .   (lint:fix to autofix)
pnpm run build:shell            # other builds: build:framework, build:webui, build:plugin-builtin, build:openapi
```

Tests are `packages/napcat-test/*.test.ts` (vitest, `vitest.config.ts` in that package).

## new-api (Go 1.26 root; Bun frontend)

Run from `reference/new-api/`:

```bash
make test                       # tests root module + relaykit + tokenkit (GOWORK=off)
make build-web                  # cd web && bun install --frozen-lockfile && bun run build
make dev-api                    # docker compose -f docker-compose.dev.yml up -d  (postgres+redis+api on :3000)
make dev-web                    # bun dev server on :5173, proxies API to :3000
make dev                        # dev-api + dev-web
make reset-setup                # reset local setup-wizard state (dev only)
```

Backend directly: `go vet ./...`, `go build ./...`, and for module-independence checks
`cd relaykit && GOWORK=off go build ./...` (same for `tokenkit/`).
Frontend (from `web/`): `bun install`, `bun run typecheck` (`tsgo -b`), `bun run test` (`vitest run`),
`bun run lint` (`oxlint -c .oxlintrc.json .`), `bun run build`, `bun run i18n:sync`.

## Conventions (observed in both repos)

- **Conventional Commits with a scope**, lowercase: `fix(xai): …`, `feat(web): …`, `chore(ci): …`,
  `refactor(jsplugin): …`, `test(macos): …`.
- Go code must stay cross-database (SQLite/MySQL/PostgreSQL) and `relaykit/`/`tokenkit/` must not
  import the root module — see `reference/new-api/AGENTS.md` for the mandatory rules.
- NapCat uses `neostandard` + semicolons, 2-space indent, trailing commas `always-multiline`;
  unused vars are errors unless prefixed `_`.
- Frontend text is always i18n'd (`useTranslation()` + `t('English key')`), locale files are flat
  JSON keyed by English source strings.

## Pitfalls

- **`GOWORK=off` is mandatory** for new-api builds/tests; the modules are wired as a workspace and
  the root build will not catch a broken `relaykit`/`tokenkit`.
- **new-api embeds `web/dist`.** Before `go build ./...` on a clean checkout, create the placeholder
  (`mkdir -p web/dist && touch web/dist/index.html`), as CI does, or the build fails.
- `make test` also needs the same embed placeholder to exist.
- **NapCat `dev` is Windows-only**: `pnpm run dev:shell` → `pnpm --filter napcat-develop run dev`,
  which runs `powershell ./nodeTest.ps1`. There is no cross-platform dev runner.
- `pnpm-lock.yaml` is gitignored in NapCatQQ; do not treat it as a committed artifact.
- Both repos are upstream clones — changes made here are not the project of record and can be
  overwritten by a future refresh.
