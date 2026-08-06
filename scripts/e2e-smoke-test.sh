#!/usr/bin/env bash
# End-to-end smoke test: proves the SAME rule ("smoke-ip-limit" in
# deploy/config/config.e2e.yaml) is enforced identically whether the
# request comes in through Kong or through APISIX - and that a request
# blocked via one gateway stays blocked via the other, since both call
# into the same centralized Unigate "brain" (PRD section 9 acceptance
# criteria; Apigee needs a live sandbox so isn't covered here).
#
# Usage:
#   docker compose -f deploy/docker/docker-compose.e2e.yaml up -d --build
#   ./scripts/e2e-smoke-test.sh
#   docker compose -f deploy/docker/docker-compose.e2e.yaml down -v
#
# Requires a freshly started stack (no prior requests against
# "smoke-ip-limit"), since the test relies on knowing the exact count
# consumed so far.
set -euo pipefail

KONG_URL="http://localhost:8000/protected"
APISIX_URL="http://localhost:9080/protected"
HEALTH_URL="http://localhost:18080/healthz"
WINDOW_LIMIT=3

pass=0
fail=0

log() { printf '[e2e] %s\n' "$1"; }
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fail=$((fail + 1)); }

wait_for() {
  local name="$1" url="$2" tries=30
  log "waiting for $name at $url"
  until curl -s -o /dev/null "$url"; do
    tries=$((tries - 1))
    if [ "$tries" -le 0 ]; then
      echo "  $name did not become reachable in time" >&2
      exit 1
    fi
    sleep 1
  done
}

# request <gateway> <url> asserts the response code and prints a PASS/FAIL line.
request() {
  local name="$1" url="$2" expect="$3" desc="$4"
  local headers code
  headers=$(curl -s -D - -o /dev/null "$url")
  code=$(printf '%s' "$headers" | head -1 | awk '{print $2}')
  if [ "$code" = "$expect" ]; then
    ok "$name: $desc (got $code)"
  else
    bad "$name: $desc (expected $expect, got $code)"
  fi
  printf '%s' "$headers"
}

assert_header() {
  local name="$1" headers="$2" header="$3"
  if printf '%s' "$headers" | grep -qi "^${header}:"; then
    ok "$name: response includes $header"
  else
    bad "$name: response missing $header"
  fi
}

wait_for unigate "$HEALTH_URL"
wait_for kong "$KONG_URL"
wait_for apisix "$APISIX_URL"

log "consuming the ${WINDOW_LIMIT}-request budget, alternating gateways"
request kong "$KONG_URL" 200 "request 1/${WINDOW_LIMIT} allowed" >/dev/null
request apisix "$APISIX_URL" 200 "request 2/${WINDOW_LIMIT} allowed (via the OTHER gateway)" >/dev/null
request kong "$KONG_URL" 200 "request 3/${WINDOW_LIMIT} allowed" >/dev/null

log "budget exhausted: next request on EITHER gateway must block"
h=$(request kong "$KONG_URL" 429 "request over budget blocked")
assert_header kong "$h" "Retry-After"
assert_header kong "$h" "X-RateLimit-Limit"

log "the other gateway must see the SAME block (shared centralized state)"
h=$(request apisix "$APISIX_URL" 429 "still blocked via the other gateway")
assert_header apisix "$h" "Retry-After"
assert_header apisix "$h" "X-RateLimit-Limit"

echo
log "results: $pass passed, $fail failed"
if [ "$fail" -gt 0 ]; then
  exit 1
fi
