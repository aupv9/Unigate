# Unigate SRE Runbook

Operational reference for on-call engineers running the Universal Rate
Limiting Service. Pairs with `deploy/observability/` (dashboard +
alerts) and `docs/PRD.md` (requirements/terminology).

## Alert reference

| Alert (`deploy/observability/prometheus/alerts.yml`) | Meaning | What to do |
|---|---|---|
| `UnigateServiceDown` | Prometheus can't scrape the service for 1m | See [Service/Redis outage](#service--redis-outage) |
| `UnigateFailModeActivated` | A rule's backing Redis call errored and it fell back to its configured `fail_open`/`fail_closed` | See [Service/Redis outage](#service--redis-outage) |
| `UnigateHighBlockRate` | A rule is blocking >5 req/s (5m avg) | See [Suspected brute-force attack](#suspected-brute-force-attack) |
| `UnigateLockoutSpike` | >20 new escalated lockouts on a rule in 10m | See [Suspected brute-force attack](#suspected-brute-force-attack) |
| `UnigateCheckLatencyHigh` | `CheckLimit` p99 > 50ms for 5m on a gateway | Check Redis latency/load first (NFR1); if Redis is healthy, check network path between that gateway and the service (should be same-region/VPC per PRD section 8) |

## Service / Redis outage

**Detect:** `UnigateServiceDown` or `UnigateFailModeActivated` fires,
or you see `"msg":"ruleengine: store error, applying fail mode"` in
the structured logs (fields: `rule_id`, `fail_mode`, `fail_open`,
`err`).

**Understand the blast radius — this is per-rule, not global:**
- A rule configured `fail_mode: fail_open` lets *all* traffic through
  unthrottled while the store is unreachable — availability is
  preserved, rate-limiting/brute-force protection is not.
- A rule configured `fail_mode: fail_closed` blocks *all* traffic for
  that rule while the store is unreachable — security posture is
  preserved, availability is not.

Check `unigate_fail_mode_total{rule_id, mode}` in Prometheus/Grafana
(the "Fail-mode by rule" table panel) to see exactly which rules are
affected and in which direction.

**Diagnose:**
1. `redis-cli -h <redis-host> ping` (or `CLUSTER INFO` if running
   Redis Cluster, NFR3) — confirm whether Redis itself is down,
   unreachable, or just slow.
2. Check for network partition between the Unigate pods and Redis
   (security groups, NetworkPolicy, DNS).
3. Check Unigate's own logs/`/healthz` — if Unigate itself is down
   (not just its Redis connection), that's a service incident, not a
   store incident; check pod restarts / OOM / panics.

**Mitigate:**
- If Redis is down: escalate to whoever owns the Redis
  Cluster/infrastructure (per PRD section 4, that's Platform/Infra).
  There is no Unigate-side fix for Redis being down.
- If a `fail_closed` rule is blocking legitimate traffic and you need
  to restore availability before Redis recovers: flip it to
  `fail_open` via the Admin API without redeploying anything (FR8):

  ```sh
  curl -s localhost:8081/v1/admin/rules/<rule-id> | jq '.fail_mode = "fail_open"' | \
    curl -s -X PUT localhost:8081/v1/admin/rules/<rule-id> -d @-
  ```

  This is a real security/availability tradeoff — for a brute-force
  rule like `login-brute-force`, flipping to fail_open means logins
  are temporarily unthrottled. Only do this if the availability impact
  outweighs the risk, and flip it back once Redis recovers.
- The change propagates to every stateless instance within one
  registry-refresh interval (5s by default, `startRuleRefreshLoop` in
  `cmd/server/main.go`) — no restart needed.

## Adding or changing a rule safely

Rules can be changed at runtime via the Admin API (FR8) without
redeploying any gateway. Still, treat it like a config change with
blast radius:

1. **Check the adapter config matches first.** The rule's `key_parts`
   must exactly match what the gateway adapter is configured to
   extract (`config.key_parts` in the Kong/APISIX plugin config, or
   `unigate.key_parts` for Apigee). A mismatch here causes
   `ErrMissingKeyPart` (400s) at the gateway, not a rate-limit bug.
2. **Test the rule directly against the service before wiring it into
   a gateway route**, e.g.:
   ```sh
   curl -s localhost:8081/v1/admin/rules -d '{"id":"test-rule", "key_parts":["ip"], "windows":[{"limit":5,"period":"1m"}]}'
   curl -s localhost:8080/v1/check -d '{"rule_id":"test-rule","key":[{"kind":"ip","value":"1.2.3.4"}]}'
   ```
3. **Every update is versioned automatically** — no need to
   hand-save the previous JSON. If new thresholds cause problems:
   ```sh
   curl -s localhost:8081/v1/admin/rules/<id>/versions           # see history (most recent first)
   curl -s -X POST localhost:8081/v1/admin/rules/<id>/rollback   # revert to the immediately preceding version
   curl -s -X POST localhost:8081/v1/admin/rules/<id>/rollback -d '{"version": 3}'  # revert to a specific version
   ```
   Rollback reapplies old content as a **new** version (like `git
   revert`), so the history stays a complete forward-only log — it
   never destructively rewinds. Up to the last 10 versions are kept
   per rule.
4. **Expect ~5s propagation**, not instant, across every instance
   (same registry-refresh mechanism as above) — this applies to
   rollbacks too.
5. Watch the Grafana dashboard's "Allow vs Block rate" and "Block rate
   by rule" panels for a few minutes after any threshold change to
   confirm it's not over-blocking legitimate traffic.

## Suspected brute-force attack

**Detect:** `UnigateHighBlockRate` or `UnigateLockoutSpike` fires, or
the Grafana "Brute-force lockouts" / "Top blocked rules" panels show a
spike.

**Investigate:**
1. Grep the structured audit log for the affected rule (FR9):
   ```
   {"msg":"rate_limit_block","rule_id":"login-brute-force","gateway":"kong","identity":"ip=203.0.113.7|username=alice","locked_out":true,"reason":"lockout",...}
   ```
   `identity` shows exactly which key (IP, username, or combination)
   is triggering blocks — this is what you'd feed to any upstream
   blocking (WAF, firewall) if the attack needs a harder stop than
   rate-limiting alone provides (out of scope for Unigate itself, see
   PRD section 3).
2. Check whether it's concentrated on one or two identities (targeted
   credential-stuffing against specific accounts) or spread across
   many (broad brute-force sweep or a misbehaving legitimate client
   retrying too aggressively — check if the identity looks like a
   known internal service/IP before assuming malicious intent).
3. Cross-check `unigate_lockouts_total` vs `unigate_blocks_total`: a
   high block rate with few actual lockouts suggests legitimate
   traffic bumping into a limit (maybe a client bug, maybe the
   threshold is too tight); a high lockout rate is the stronger
   brute-force signal, since it means the same identity keeps getting
   re-blocked past escalating cooldowns.

**Respond:**
- The escalating lockout (FR5) is already containing the attack
  automatically — confirm it's escalating as configured
  (`lockout.steps` in the rule) rather than resetting.
- If you need to manually clear state for a specific key (e.g. a
  false-positive lockout on a real user), use `POST /v1/reset` with
  that exact key — this is intended for this kind of operational
  correction, not for blocking further attempts.
- If the attack is severe enough to need blocking above what
  rate-limiting can do (mass IP block, WAF rule), that's outside
  Unigate's scope (PRD section 3) — hand off to whatever network/WAF
  layer your org uses, using the identities gathered above.

## Escalation

Fill in your org's real paging destinations here — this repo doesn't
know them:
- Redis/infrastructure incidents → Platform/Infra on-call
- Suspected active attack needing action beyond rate-limiting → Security team
- Latency/availability regressions in the service itself → whoever owns this repo's on-call rotation
