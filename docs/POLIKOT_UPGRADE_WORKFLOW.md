# Polikot CLIProxyAPI upgrade workflow

This repository is kept as a **clean upstream + Polikot overlay**.  Do not upgrade by
pulling upstream into a dirty working tree.

## Current upgrade baseline

- Upstream baseline: `origin/main` tag `v7.2.111` (commit `4a315136`).
- Polikot upgrade branch: `polikot/full-v7-upgrade-20260731_104704`.
- Production remains on the previous build until the verification and deploy steps below pass:
  - version: `v7.2.67-polikot`
  - commit: `6106620f7dcd`
- Pre-upgrade rollback snapshot:
  `/Users/aipolikot/myai/RoutingMainKeys/backups/pre-full-upgrade-20260731_104704`.
- Note: production runs with `-local-model`, so the model registry is frozen at build
  time (embedded `internal/registry/models/models.json`). New upstream models (e.g.
  `grok-4.5`) only appear after rebuilding and redeploying from a newer baseline.
- Runtime binary: `/Users/aipolikot/myai/RoutingMainKeys/state/cliproxy/bin/cliproxyapi`.
- Runtime config and OAuth state are **not** committed to this repository.

## Committed Polikot overlay

Keep Polikot changes as explicit commits on top of upstream:

1. `2b228c92 chore(polikot): replay local customizations on v7.2.111`
   - account pools / client access / generic secrets config
   - pool-aware routing metadata and selectors
   - auth file pause support
   - legacy in-memory usage statistics for the dashboard
   - Codex rate-limit management endpoint
2. `a710b88c fix(polikot): register custom management routes after v7.2.111 upgrade`
   - registers `/v0/management/account-pools`
   - registers `/v0/management/client-access`
   - registers `/v0/management/account-matrix`
   - registers `/v0/management/generic-secrets`
   - registers `/v0/management/codex-rate-limits`
   - registers `/v0/management/auth-files/pause`
   - includes a regression test that these routes do not return 404
3. `f4118c3c feat(polikot): preserve custom Claude API-key base URLs`
   - uses `x-api-key` for Anthropic-compatible API-key endpoints
   - preserves custom base paths
   - avoids forcing the official `?beta=true` query and Claude OAuth fingerprint
     headers onto custom gateways
   - covers both request preparation and end-to-end executor behavior

Future custom fixes should be separate commits with `polikot` in the subject when they are
part of the local overlay.

## Dirty-tree policy

Before every upstream update:

```bash
cd /Users/aipolikot/myai/RoutingMainKeys/CLIProxyAPI
git status --short --branch
git diff --stat
```

Rules:

- Tracked files must be clean before fetching/cherry-picking upstream.
- Do not commit runtime secrets, auth files, logs, binaries, or `state/cliproxy` data.
- `graphify-out/` is generated and ignored. It may exist locally for navigation, but must not
  make the working tree dirty.
- If a generated file appears in `git status`, either add it to `.gitignore` or delete it;
  do not mix generated artifacts with code commits.

## Safe update sequence

```bash
cd /Users/aipolikot/myai/RoutingMainKeys/CLIProxyAPI
TS=$(date +%Y%m%d_%H%M%S)
mkdir -p /Users/aipolikot/myai/RoutingMainKeys/backups/pre-upgrade-$TS

git status --short --branch
git bundle create /Users/aipolikot/myai/RoutingMainKeys/backups/pre-upgrade-$TS/CLIProxyAPI.bundle --all
git diff > /Users/aipolikot/myai/RoutingMainKeys/backups/pre-upgrade-$TS/CLIProxyAPI.diff

git fetch origin
# Create a new upgrade branch from latest upstream, then cherry-pick the Polikot overlay commits.
git switch -c polikot/full-vX-upgrade-$TS origin/main
git cherry-pick <previous-polikot-overlay-commits>
```

Resolve conflicts in small commits.  Never hide a route registration conflict: handler files
can compile while dashboard routes still return 404.

## Required verification before deploy

At minimum run:

```bash
go test ./internal/api ./internal/api/handlers/management ./sdk/cliproxy/auth ./internal/config ./internal/runtime/executor ./internal/runtime/executor/helps ./sdk/api/handlers
go test ./...
go build -o /tmp/cliproxyapi-upgraded ./cmd/server
```

Do not ignore failures in the Polikot-critical packages listed above. If `go test ./...`
finds an unrelated upstream-only failure, document it explicitly before deciding whether
deployment is safe.

## Deploy sequence

```bash
cd /Users/aipolikot/myai/RoutingMainKeys/CLIProxyAPI
COMMIT=$(git rev-parse --short=12 HEAD)
BUILT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION=vX.Y.Z-polikot

go build -ldflags "-X main.Version=$VERSION -X main.Commit=$COMMIT -X main.BuildDate=$BUILT" \
  -o /tmp/cliproxyapi-$VERSION ./cmd/server

STATE=/Users/aipolikot/myai/RoutingMainKeys/state/cliproxy
cp "$STATE/bin/cliproxyapi" "$STATE/bin/cliproxyapi.bak-$VERSION-predeploy"
install -m 755 /tmp/cliproxyapi-$VERSION "$STATE/bin/cliproxyapi.new"
mv "$STATE/bin/cliproxyapi.new" "$STATE/bin/cliproxyapi"
launchctl kickstart -k gui/$(id -u)/com.cliproxyapi.central-keys
```

## Required live smoke after deploy

Use the management key from `state/cliproxy/dashboard-login.json`.

```bash
curl -fsS http://127.0.0.1:8317/healthz
curl -fsS -H "Authorization: Bearer $MGMT" http://127.0.0.1:8317/v0/management/auth-files
curl -fsS -H "Authorization: Bearer $MGMT" http://127.0.0.1:8317/v0/management/account-pools
curl -fsS -H "Authorization: Bearer $MGMT" http://127.0.0.1:8317/v0/management/client-access
curl -fsS -H "Authorization: Bearer $MGMT" http://127.0.0.1:8317/v0/management/account-matrix
curl -fsS -H "Authorization: Bearer $MGMT" http://127.0.0.1:8317/v0/management/generic-secrets
curl -fsS -H "Authorization: Bearer $MGMT" http://127.0.0.1:8317/v0/management/codex-rate-limits
curl -fsS -H "Authorization: Bearer $MGMT" http://127.0.0.1:8317/v0/management/usage
```

Also smoke at least one Codex request for each main client key:

- `ogod`
- `openclaw-main`
- `openclaw-foxy`
- `hermesops`

Claude failures should be diagnosed separately from the upgrade if `/auth-files` shows Claude
OAuth accounts as disabled/error.
