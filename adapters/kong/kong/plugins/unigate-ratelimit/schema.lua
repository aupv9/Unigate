-- Config schema for the "unigate-ratelimit" Kong plugin (FR6).
--
-- This plugin is a thin adapter: it extracts the composite key Unigate's
-- rule expects (IP / consumer / arbitrary header), calls the central
-- CheckLimit HTTP endpoint, and maps the decision back onto the Kong
-- request/response lifecycle.
local typedefs = require "kong.db.schema.typedefs"

return {
  name = "unigate-ratelimit",
  fields = {
    { protocols = typedefs.protocols_http },
    {
      config = {
        type = "record",
        fields = {
          { service_url = { type = "string", required = true, default = "http://unigate:8080/v1/check" } },
          { reset_url = { type = "string", required = false } },
          { rule_id = { type = "string", required = true } },
          { namespace = { type = "string", required = false } },
          { gateway_name = { type = "string", required = true, default = "kong" } },
          -- Sent as X-Unigate-Api-Key (NFR5). Mark referenceable so it
          -- can be stored via Kong Vault instead of plaintext.
          { api_key = { type = "string", required = false, referenceable = true } },
          {
            key_parts = {
              type = "array",
              required = true,
              default = { "ip" },
              elements = {
                type = "string",
                -- "ip", "consumer_username", or "header:<Header-Name>"
                match = "^(ip|consumer_username|header:.+)$",
              },
            },
          },
          { cost = { type = "integer", default = 1, gt = 0 } },
          { timeout_ms = { type = "integer", default = 50, gt = 0 } },
          -- Adapter-level circuit breaker: what to do if the Unigate
          -- service itself is unreachable/times out (independent of the
          -- rule's own server-side fail_open/fail_closed, which only
          -- applies once the request reaches the service).
          { fail_open = { type = "boolean", default = true } },
        },
      },
    },
  },
}
