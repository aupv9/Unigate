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

## Regenerating protobuf code

```sh
make proto   # requires protoc, protoc-gen-go, protoc-gen-go-grpc
```
