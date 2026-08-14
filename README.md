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

Every update is versioned automatically (up to the last 10 per rule),
and can be rolled back without hand-editing thresholds back in:

```sh
curl -s localhost:8081/v1/admin/rules/login-brute-force/versions | jq
curl -s -X POST localhost:8081/v1/admin/rules/login-brute-force/rollback   # -> previous version
```

Rollback reapplies old content as a **new** version (like `git
revert`) rather than destructively rewinding, so the history stays a
complete audit trail - see `docs/RUNBOOK.md`. Verified in
`internal/ruleengine/registry_test.go`, including that version numbers
and history converge correctly across multiple stateless instances
sharing the same Redis (a real cross-instance test, not just
single-process).

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

### mTLS

NFR5 can also be satisfied with native mutual TLS instead of (or
alongside) the API-key auth above - see the commented `server.tls`
block in `deploy/config/config.yaml`. It applies uniformly to the
gRPC, CheckLimit HTTP, and Admin HTTP listeners (not the metrics
endpoint, which assumes internal-network-only Prometheus scraping).
Set `require_client_cert: false` for server-side-only TLS, or `true`
plus `client_ca_file` for full mTLS where every adapter must present a
certificate signed by that CA. Verified in
`internal/tlsutil/tlsutil_test.go` (generates a real CA + certs and
proves the handshake actually rejects missing/untrusted client certs,
not just that the config parses) and manually against the running
binary with `curl --cert/--key` and a raw gRPC client.

### Tracing

OpenTelemetry distributed tracing across the whole `CheckLimit` path -
gRPC/HTTP request → rule engine evaluation → each Redis Lua call - via
the `tracing` block in `deploy/config/config.yaml`, exported over
OTLP/HTTP to any collector (OpenTelemetry Collector, Jaeger, Tempo).
Disabled by default. Instrumentation lives in `internal/tracing`
(provider setup), `internal/ruleengine/engine.go` (the top-level
`CheckLimit` span), `internal/store/*.go` (a child span per Redis
call - `redis.sliding_window`, `redis.gcra`, `redis.lockout.*`), and
`cmd/server/main.go` (`otelgrpc`/`otelhttp` wrapping the transport
layers). Verified two ways: `internal/ruleengine/tracing_test.go` uses
an in-memory span exporter to prove `CheckLimit` produces a span with
the expected attributes *and* that the store's Redis call shows up as
its child (real context propagation, not just a span existing); and
manually, by pointing the running binary at a throwaway local HTTP
server standing in for an OTLP collector and confirming it actually
receives protobuf-encoded trace data after a request.

## Testing

```sh
make test   # spins up an ephemeral redis-server per test package
make vet
```

Every layer has coverage: `internal/config`, `internal/store` (sliding
window / GCRA / lockout, atomicity), `internal/ruleengine` (fail-open/
closed, lockout escalation), the API surface gateways actually call
(`internal/api/httpserver`, `internal/api/grpcserver`,
`internal/api/adminserver`, `internal/api/authmw`), and mTLS enforcement
(`internal/tlsutil`).

`internal/store/cluster_test.go` goes further than `cluster_mode:
true` in isolation - it spins up a **real 3-master Redis Cluster**
(`redis-server --cluster-enabled yes` + `redis-cli --cluster create`)
and re-runs the sliding-window/GCRA/lockout checks against it (NFR3).
This matters because the sliding-window script takes multiple `KEYS`
in one Lua call; under real cluster sharding that fails with
`CROSSSLOT` unless every key hashes to the same slot, so this is what
actually proves the `{namespace:ruleID:identity}` hash-tag design
(`internal/store/client.go`) works, rather than just asserting it by
inspection. It also confirms different identities really do land on
different nodes (not all funneled onto one).

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

## Load testing (NFR1)

```sh
go run ./cmd/loadtest -concurrency 50 -duration 10s
```

A small dependency-free Go tool that hammers `POST /v1/check` and
reports latency percentiles (p50/p90/p99/p999), throughput, and error
rate. See `docs/LOAD_TEST.md` for flags, methodology, and an example
run - which is explicitly **not** a representative NFR1 baseline (ran
on a shared dev sandbox, not co-located dedicated infrastructure); use
the tool to establish your own baseline in the target environment.

## Regenerating protobuf code

```sh
make proto   # requires protoc, protoc-gen-go, protoc-gen-go-grpc
```
