# uptime-monitor

A small uptime monitor written in Go for my own live sites, plus a `verify` command that CI runs after a deploy to decide whether a release is healthy.

- **Monitor:** checks each site on its own interval, stores every result in PostgreSQL, and serves a JSON API and a status page.
- **Release check:** `uptime-monitor verify` polls a health URL after a deploy and exits non-zero on any failure or a slow p95, so the pipeline can fail the job and roll back.

Standard library first. The only dependencies are `pgx` (Postgres) and `yaml.v3`.

## Architecture

```
sites.yaml ──► config.Load
                   │
                   ▼
        checker.Run ── one goroutine + ticker per site
                   │      each check: GET with context.WithTimeout, fresh connection
                   ▼
            chan Result ──► main loop ──► store.Insert ──► PostgreSQL (checks table)
                                                              │
       net/http (Go 1.22 routing) ◄── store.Stats / Latest ◄──┘
         GET /                         status page (html/template)
         GET /api/sites                sites + last check
         GET /api/sites/{name}/stats   uptime %, p50/p95 (computed in SQL)
         GET /healthz                  monitor's own health (DB ping)

verify ── checker.Check × N ──► report + exit code (0 healthy, 1 unhealthy, 2 usage)
```

| Package | What it does |
|---|---|
| `internal/config` | Loads and validates `sites.yaml` (defaults: 30 s interval, 10 s timeout) |
| `internal/checker` | `Check` (one GET) and `Run` (goroutine per site, graceful shutdown) |
| `internal/store` | pgxpool, embedded SQL migrations, uptime/percentile queries |
| `internal/api` | JSON endpoints and the server-rendered status page |
| `internal/verify` | Post-deploy release check and percentile math |

**Design notes**
- **Graceful shutdown:** on SIGINT/SIGTERM the tickers stop, in-flight checks finish (they run on `context.WithoutCancel` with their own timeout), the last results are written, then the HTTP server shuts down.
- **Fresh connection per check:** keep-alive is disabled. In testing, reused HTTP/2 connections that sat idle between checks went dead without the client noticing, and the next check hung until its timeout (see *Measured results*). Latency therefore includes DNS + TCP + TLS, like a first visit.
- **Stats in SQL:** uptime % and p50/p95 use `percentile_cont` over successful checks only, so timeouts count against uptime but don't distort latency. `verify` computes percentiles in Go with the same interpolation; a test checks that both give identical results on the same data.

## Run it locally

Needs Docker. Go 1.22+ is only needed to run tests or the binary outside Docker.

```bash
cp .env.example .env              # then set a real POSTGRES_PASSWORD (and the same in the URLs)
docker compose up -d --build      # Postgres + monitor
```

Open http://localhost:8080 for the status page.

```bash
curl localhost:8080/api/sites
curl "localhost:8080/api/sites/taskforge/stats?window=24h"    # window: 90m, 24h, 7d …
```

Sites live in `sites.yaml`. Mount your own file and point `CONFIG_PATH` at it to change them without rebuilding.

**Tests**
```bash
docker compose up -d db
TEST_DATABASE_URL=postgres://uptime:<password>@localhost:5433/uptime?sslmode=disable go test -race ./...
```
Without `TEST_DATABASE_URL` the Postgres integration tests are skipped. `-race` needs cgo (a C compiler); on Windows run it in a `golang:1.22` container.

## verify in CI

```bash
uptime-monitor verify --url https://taskforge.136.80.52.40.sslip.io/healthz \
  --checks 10 --interval 3s --max-p95 800ms
```

```
verify https://taskforge.136.80.52.40.sslip.io/healthz  checks=10 interval=3s max-p95=2s
  #1  200     906ms  ok
  ...
  #10 200     913ms  ok
checks=10 failures=0 p50=1.034s p95=1.479s
PASS
```

It fails (exit 1) if any check fails, if p95 is over `--max-p95`, or if the job is cancelled midway. Bad flags exit 2.

[`docs/ci-example.yml`](docs/ci-example.yml) shows a TaskForge release workflow. It builds and pushes the images, runs `kubectl apply -k k8s/prod`, and waits for the rollouts. It then runs `verify` against the public URL and runs `kubectl rollout undo` if verify fails.

Pods being *Ready* only proves the in-cluster probes pass. `verify` tests the whole public path: DNS, load balancer, ingress, TLS and the app.

Point `verify` at a real health endpoint. TaskForge returns 200 for *any* path (unknown paths fall through to the admin web app), so a typo'd URL would always "pass".

## CI

`.github/workflows/ci.yml` runs on every push and pull request:
1. gofmt check, `go vet`, `staticcheck`
2. `go test -race` against a Postgres service container
3. Builds the Docker image (multi-stage, `distroless/static`, nonroot) and smoke-tests it

A manual `verify-live` job (Actions → ci → Run workflow) runs `verify` against TaskForge from a GitHub runner. It's kept off push builds so a slow external site can't break CI for code changes.

## Measured results

All numbers come from real runs on 2026-11-05, from a home PC in India.

**Monitor:** 139 checks over about 22 minutes (16:58–17:20 IST, with short gaps while I restarted it), 0 failures:

| Site | Checks | Failures | Uptime | p50 | p95 |
|---|---|---|---|---|---|
| snap-to-pay | 39 | 0 | 100% | 232 ms | 1167 ms |
| codepulse `/health` | 39 | 0 | 100% | 1053 ms | 2156 ms |
| oauth-ready | 22 | 0 | 100% | 189 ms | 506 ms |
| taskforge `/healthz` | 39 | 0 | 100% | 914 ms | 1767 ms |

These latencies include a fresh TLS handshake from India to US-hosted servers, so they reflect distance as much as server speed. The sample is small.

**Stale keep-alive bug:** the first version shared one HTTP client with keep-alive on. The two Vercel sites "failed" with 10 s timeouts, but only when their checks ran at the same moment. A side test hit both sites 4 times each with each client type: keep-alive failed **3 of 8** requests, fresh connections failed **0 of 8**. After switching to fresh connections, 139 checks had 0 failures.

**verify against TaskForge** (10 checks, 3 s apart, run twice):
- `--max-p95 800ms`: 10/10 returned 200, p50 1.003 s, **p95 1.449 s → FAIL** (exit 1)
- `--max-p95 2s`: 10/10 returned 200, p50 1.034 s, **p95 1.479 s → PASS** (exit 0)
- against a real 404 (CodePulse `/nope`): 3/3 failed → FAIL (exit 1)

From this PC, an 800 ms gate fails on distance alone. A CI runner in the US is much closer to the `us-central1` cluster; run `verify-live` to measure that before choosing the limit.

**Image:** 22.9 MB. Inside Docker the monitor checks its own health with `verify` against `/healthz`, since the image has no shell or curl.

## Configuration

| Env var | Used by | Default |
|---|---|---|
| `DATABASE_URL` | `run` (required) | — |
| `CONFIG_PATH` | `run` | `sites.yaml` |
| `ADDR` | `run` | `:8080` |
| `TEST_DATABASE_URL` | integration tests | unset = skip |
| `POSTGRES_PASSWORD` | docker compose | — (in `.env`, git-ignored) |

No secrets are committed. `.env` is git-ignored, and the password in `ci.yml` exists only for the throwaway test database.
