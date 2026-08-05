# Apigee adapter: ServiceCallout + JavaScript

Apigee doesn't allow arbitrary compiled plugin code (see
`docs/PRD.md` section 8), so the adapter is built entirely from
standard policies (FR6):

| Policy | Type | Purpose |
|---|---|---|
| `JS-Unigate-BuildRequest` | JavaScript | Extracts the composite key from flow variables and builds the CheckLimit JSON body |
| `AM-Unigate-Request` | AssignMessage | Wraps that body into the outbound HTTP message (headers incl. `X-Unigate-Api-Key`, NFR5) |
| `SC-Unigate-CheckLimit` | ServiceCallout | Calls Unigate's `POST /v1/check` |
| `JS-Unigate-EnforceDecision` | JavaScript | Parses the response, decides allow/block, sets header flow variables |
| `RF-Unigate-RateLimited` | RaiseFault | On block: returns 429/503 with `Retry-After` + `X-RateLimit-*` (FR7) |
| `AM-Unigate-SetHeaders` | AssignMessage | On allow: forwards `X-RateLimit-*` on the real response |

See `flow-snippet-example.xml` for the exact Step/Condition wiring —
note that the block decision (`RF-Unigate-RateLimited`) must run in the
**Request** PreFlow (before the target is called), while
`AM-Unigate-SetHeaders` must run in the **Response** PostFlow, since
Apigee's response message object doesn't exist yet during request
processing.

## Install

1. Copy `policies/*.xml` into your proxy bundle's `apiproxy/policies/`.
2. Copy `jsc/*.js` into `apiproxy/resources/jsc/`.
3. Register `targetserver-unigate.xml` in each environment (points at
   Unigate's HTTP listener, `config.yaml server.http_addr`).
4. Wire the steps into your `ProxyEndpoint` per `flow-snippet-example.xml`.
5. Before each proxy's PreFlow reaches `JS-Unigate-BuildRequest`, set
   `unigate.rule_id` and `unigate.key_parts` (comma-separated: `ip`,
   `consumer_username`, `header:<Name>`) — typically via a small
   AssignMessage step per API product — and source `unigate.api_key`
   from a KVM or Vault entry rather than hardcoding it.
