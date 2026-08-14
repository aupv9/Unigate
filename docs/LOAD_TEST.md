# Load testing (NFR1 / NFR2)

`cmd/loadtest` is a small, dependency-free Go tool that hammers a
running Unigate instance's `POST /v1/check` with concurrent workers
and reports latency percentiles + throughput + error rate.

```sh
go run ./cmd/loadtest -concurrency 50 -duration 10s -rule-id anonymous-ip-limit -key-cardinality 2000
```

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `-url` | `http://localhost:8080/v1/check` | target endpoint |
| `-rule-id` | `anonymous-ip-limit` | rule to evaluate |
| `-gateway` | `loadtest` | gateway label sent in each request (shows up in metrics/audit) |
| `-concurrency` | `50` | concurrent workers |
| `-duration` | `10s` | run length |
| `-key-cardinality` | `500` | number of distinct synthetic IPs cycled through, so requests don't all collide on one rate-limit key |

## Methodology

Each worker loops for the full duration, picking a random synthetic IP
(`10.0.x.y`, `x.y` derived from `-key-cardinality`) and sending one
`CheckLimit` request per iteration over a connection-pooled
`http.Client` (`MaxIdleConnsPerHost` sized to concurrency, so the test
isn't bottlenecked on TCP/TLS handshakes it isn't trying to measure).
Wall-clock latency is measured per-request from just before
`client.Do` to just after the response body is fully read. Both `200`
and `429` responses count toward the latency numbers - the rate-limit
script does real work on the Redis side either way, so both are valid
samples of `CheckLimit`'s added latency (NFR1); they're also reported
by status code so you can see the allow/block mix.

## Example run (NOT a representative baseline)

Against `deploy/config/config.local.yaml` (single-node Redis, sliding
window / GCRA rules from that file), on **this repo's shared/virtualized
dev sandbox** - not dedicated same-region hardware:

```
$ go run ./cmd/loadtest -concurrency 50 -duration 10s -rule-id anonymous-ip-limit -key-cardinality 2000
Unigate load test
  target:        http://localhost:8080/v1/check
  rule_id:       anonymous-ip-limit
  concurrency:   50
  duration:      10s (actual: 10.001s)
  total requests: 201924 (20190 req/s)
  errors:        50 (0.02%)
  status codes:  map[200:71738 429:130136]

  latency p50:   2.076113ms
  latency p90:   4.483599ms
  latency p99:   7.574389ms
  latency p999:  10.91177ms
  latency max:   35.78328ms
```

**This is not the NFR1 baseline** - NFR1 targets p99 <= 5ms "when the
service is co-located with the gateway" on real infrastructure; a
shared dev sandbox with a single Redis instance on the same host isn't
that environment, and the ~7.5ms p99 above is expected to differ
(better or worse) from a real deployment. The 50 errors (0.02%) are
requests still in flight when the test's duration context expired, not
service failures.

The high `429` ratio here is an artifact of the test parameters
(`-key-cardinality 2000` against a 100 req/min limit at 20k req/s
overall throughput means most IPs get reused well within their window)
- lower the concurrency/throughput or raise `-key-cardinality` for a
scenario closer to steady-state legitimate traffic; the point of this
run is latency, which is measured identically either way.

**Use this tool to establish your own real baseline**: run it from a
host in the same region/VPC as your actual deployment, against a
realistically-sized Redis (ideally the real Cluster topology, not a
single node), with `-rule-id` set to your actual highest-traffic rule.
Compare p99 against the 5ms NFR1 target and the Grafana "CheckLimit
latency" panel (`deploy/observability/`) for what real traffic looks
like over time.
