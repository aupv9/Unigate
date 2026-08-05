# APISIX adapter: `unigate-ratelimit`

Custom APISIX plugin (FR6), functionally identical to the Kong adapter:
extracts a composite key (`ip`, `consumer_username`, `header:<Name>`),
calls Unigate's `CheckLimit` HTTP endpoint, and maps the decision onto
the request (429 + `Retry-After` + `X-RateLimit-*` on block, forwarded
headers on allow — FR7).

## Install

1. Copy `apisix/plugins/unigate-ratelimit.lua` into your APISIX
   deployment's `apisix/plugins/` directory (or mount this repo's
   `adapters/apisix/apisix/plugins` over it).
2. Add `unigate-ratelimit` to the `plugins` list in `config.yaml`.
3. Attach it to a route:

   ```json
   {
     "plugins": {
       "unigate-ratelimit": {
         "service_url": "http://unigate:8080/v1/check",
         "rule_id": "login-brute-force",
         "gateway_name": "apisix",
         "key_parts": ["ip", "consumer_username"],
         "api_key": "change-me-apisix"
       }
     }
   }
   ```

4. Reload APISIX (`apisix reload`).
