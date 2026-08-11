# Unigate — Universal Rate Limiting Service

A centralized rate-limit & brute-force protection "brain" for
multi-gateway systems (Kong / APISIX / Apigee). Gateways call a single
`CheckLimit` API instead of each running its own rate-limit engine, so
security rules (e.g. brute-force lockout) stay consistent everywhere.

See `docs/PRD.md` for the full product requirements this implements.

## Architecture

```
Kong/APISIX/Apigee adapters --(gRPC or HTTP)--> Unigate service --> Redis
```

- **`cmd/server`** — the stateless service binary (NFR3): gRPC + HTTP
  `CheckLimit` API, an Admin API for rule CRUD, and a Prometheus
  `/metrics` endpoint.
- **`internal/ruleengine`** — turns a `(rule, composite key)` pair into
  an allow/deny decision: builds the identity from IP/username/API-key
  components (FR3), evaluates one or more time windows per rule (FR2),
  and escalates brute-force lockouts (FR5).
- **`internal/store`** — Redis primitives, each enforced atomically via
  Lua script (NFR4): a sliding-window log (`sliding_window.lua`), a
  GCRA token bucket (`gcra.lua`), and progressive lockout state
  (`lockout.lua`).
- **`adapters/`** — thin, gateway-specific glue: a Kong Lua plugin, an
  APISIX Lua plugin, and an Apigee ServiceCallout+JavaScript policy set
  (FR6). Each just extracts an identifier and calls `CheckLimit`.

## Requirements implemented

| Requirement | Where |
|---|---|
| FR1 gRPC + HTTP `CheckLimit` | `proto/ratelimit/v1`, `internal/api/grpcserver`, `internal/api/httpserver` |
| FR2 multiple windows per key | `internal/store/sliding_window.lua` |
| FR3 composite key (IP/username/...) | `internal/ruleengine/key.go` |
| FR4 sliding window + GCRA | `internal/store/sliding_window.lua`, `internal/store/gcra.lua` |
| FR5 progressive lockout | `internal/store/lockout.lua`, `internal/ruleengine/engine.go` |
| FR6 Kong/APISIX/Apigee adapters | `adapters/kong`, `adapters/apisix`, `adapters/apigee` |
| FR7 standard rate-limit headers | `internal/api/httpserver`, adapter code |
| FR8 admin API (rule CRUD) | `internal/api/adminserver`, `internal/ruleengine/registry.go` |
| FR9 audit log + metrics | `internal/audit`, `internal/metrics` |
| FR10 fail-open/fail-closed per rule | `internal/ruleengine/engine.go` |
| NFR4 atomic Redis ops | `internal/store/*.lua` |
| NFR5 adapter authentication | `internal/api/authmw` |
| NFR7 namespacing | `RuleConfig.Namespace`, Redis key hashing in `internal/store` |

## Running locally

Requires Go 1.25+ and a local `redis-server`.

```sh
# start Redis separately, e.g.: redis-server --daemonize yes
make run   # builds and runs against deploy/config/config.local.yaml
```

Or via Docker Compose (Redis + service):

```sh
make docker-up
```

### Try it

```sh
curl -s localhost:8080/v1/check -d '{
  "rule_id": "login-brute-force",
  "key": [{"kind":"ip","value":"1.2.3.4"}, {"kind":"username","value":"alice"}],
  "gateway": "kong"
}' | jq
```

Rules are defined in `deploy/config/config.yaml` (see that file for
FR2/FR3/FR4/FR5 example rules) and can also be managed at runtime
without redeploying via the Admin API:

```sh
curl -s localhost:8081/v1/admin/rules | jq
```

### API reference

`api/openapi.yaml` is the OpenAPI 3.0 spec for the full HTTP surface
(`/v1/check`, `/v1/reset`, `/healthz`, and the `/v1/admin/rules*`
CRUD endpoints) - schemas match the Go DTOs exactly, including the
`Rule` shape used by the Admin API. View it with any OpenAPI tool, e.g.:

```sh
npx @redocly/cli preview-docs api/openapi.yaml
```

It's linted in CI (`make openapi-lint`, requires
`pip install openapi-spec-validator`) so it can't silently drift from
the code. The equivalent gRPC API is defined in
`proto/ratelimit/v1/ratelimit.proto`.

### Secrets

`deploy/config/config.yaml` never contains real secrets: Redis
password and per-gateway API keys are written as `${VAR}` /
`${VAR:-default}`, expanded from the process environment at load time
(`internal/config/envexpand.go`) - the same syntax as shell/
docker-compose parameter expansion. The `:-change-me-*` fallbacks only
exist so local dev works with zero setup; never let them reach a real
deployment.

To set real values for `make docker-up`, copy `.env.example` to `.env`
(gitignored) at the repo root and fill it in - `docker-compose.yaml`
loads it into the `unigate` container's environment. Outside Docker,
just export the same variables before running the binary.

## Testing

```sh
make test   # spins up an ephemeral redis-server per test package
make vet
```

Every layer has coverage: `internal/config`, `internal/store` (sliding
window / GCRA / lockout, atomicity), `internal/ruleengine` (fail-open/
closed, lockout escalation), and the API surface gateways actually call
(`internal/api/httpserver`, `internal/api/grpcserver`,
`internal/api/adminserver`, `internal/api/authmw`).

CI (`.github/workflows/ci.yml`) runs `go build`/`go vet`/`gofmt`/
`go test` on every PR, plus a proto-regen diff check and Lua/JS/XML
syntax checks on the adapter code.

### End-to-end smoke test (real Kong + APISIX)

`deploy/docker/docker-compose.e2e.yaml` brings up Redis, Unigate, a
plain nginx backend, and **real** Kong + APISIX containers with the
adapters from `adapters/kong` and `adapters/apisix` mounted in, both
routing `/protected` through the `smoke-ip-limit` rule
(`deploy/config/config.e2e.yaml` — same rule engine, short window so
the test finishes in seconds). `scripts/e2e-smoke-test.sh` then proves
the two gateways enforce that one shared rule identically: it spends
the request budget across both gateways, and confirms a request
blocked via one gateway is *also* blocked via the other — i.e. the
centralized "brain" is really shared, not per-gateway.

```sh
docker compose -f deploy/docker/docker-compose.e2e.yaml up -d --build
./scripts/e2e-smoke-test.sh
docker compose -f deploy/docker/docker-compose.e2e.yaml down -v
```

**Not yet run in this repo's dev environment** (no Docker daemon
available there) — the compose files and script are syntax-validated
(`docker compose config`, `shellcheck`, and a parsing dry-run against a
local test server) but need a real run in an environment with Docker
before being trusted as a release gate. Apigee isn't covered here since
it has no local-container story; validate that adapter against a real
Apigee sandbox separately.

## Observability (G6: security team dashboard + alerting)

`deploy/observability/docker-compose.observability.yaml` brings up
Unigate + Redis + Prometheus + Grafana, with Prometheus pre-configured
to scrape `/metrics` and evaluate `prometheus/alerts.yml`, and Grafana
pre-provisioned with the Prometheus datasource and a ready-made
"Unigate Overview" dashboard (`grafana/dashboards/unigate.json`):
allow/block rate, block rate by rule, brute-force lockouts, p50/p99
`CheckLimit` latency by gateway, fail-open/closed activations, and a
top-blocked-rules table.

```sh
docker compose -f deploy/observability/docker-compose.observability.yaml up -d --build
open http://localhost:3000   # Grafana (anonymous viewer access enabled)
open http://localhost:9091   # Prometheus
```

Alert rules (`deploy/observability/prometheus/alerts.yml`, verified
with `promtool check rules`) cover: the service being unreachable, a
rule falling back to fail_open/fail_closed (Redis trouble), a spike in
blocks or lockouts on a rule (possible attack), and high `CheckLimit`
p99 latency. See `docs/RUNBOOK.md` for what to do when one fires.

**Not yet run live** here either (same no-Docker-daemon caveat as the
e2e stack above) — validated via `docker compose config`, `promtool
check rules`/`check config`, and a PromQL syntax check of every
dashboard panel expression against the real metric names in
`internal/metrics/metrics.go`.

## Kubernetes deployment (Helm)

`deploy/helm/unigate` is a Helm chart for running the service in a
real cluster (NFR3: stateless, scale horizontally): a `Deployment` +
`Service` + `ConfigMap` (rendering the same `config.yaml` shape as
`deploy/config/config.yaml`, with `${VAR}` secret placeholders per the
[Secrets](#secrets) section above), an `HPA` targeting CPU (enabled by
default), and a `PodDisruptionBudget` (NFR2). Redis password and
per-gateway API keys are sourced from an existing `Secret` you provide
via `redis.passwordSecretName` / `auth.existingSecret` — never stored
in the chart itself.

```sh
helm lint deploy/helm/unigate
helm template unigate deploy/helm/unigate | less   # preview the rendered manifests
helm install unigate deploy/helm/unigate \
  --set image.repository=<your-registry>/unigate \
  --set image.tag=<your-tag> \
  --set redis.addrs='{redis.your-namespace.svc:6379}'
```

Ships with the same two example rules as `deploy/config/config.yaml`
(`values.yaml`'s `rules:`) so a default install is immediately
functional; override `rules` with your real set. **Not run against a
live cluster here** (no cluster available in this dev environment) —
validated with `helm lint`, `helm template` (both with and without
secrets/autoscaling set), and `kubeconform -strict` against the
rendered manifests.

## Regenerating protobuf code

```sh
make proto   # requires protoc, protoc-gen-go, protoc-gen-go-grpc
```
