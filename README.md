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

## Regenerating protobuf code

```sh
make proto   # requires protoc, protoc-gen-go, protoc-gen-go-grpc
```
