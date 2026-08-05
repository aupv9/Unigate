# Kong adapter: `unigate-ratelimit`

Custom Kong plugin (FR6) that calls the Unigate rate-limit service's
`CheckLimit` HTTP endpoint and maps the decision onto the request:

- Extracts a composite key from `config.key_parts` (`ip`,
  `consumer_username`, or `header:<Name>`).
- POSTs to `config.service_url` (default `http://unigate:8080/v1/check`).
- On block: returns `429` with `Retry-After` + standard `X-RateLimit-*`
  headers (FR7).
- On allow: forwards the same headers and lets the request continue.
- `config.fail_open` controls adapter behavior if Unigate itself is
  unreachable (independent from the rule's own server-side fail mode).

## Install (declarative / DB-less Kong)

1. Mount this directory's `kong/plugins/unigate-ratelimit` onto Kong's
   Lua path, e.g. via the `KONG_PLUGINS` and `KONG_LUA_PACKAGE_PATH`
   env vars:

   ```
   KONG_PLUGINS=bundled,unigate-ratelimit
   KONG_LUA_PACKAGE_PATH=/adapters/kong/?.lua;/adapters/kong/?/init.lua;;
   ```

2. Enable it on a route/service in `kong.yml`:

   ```yaml
   plugins:
     - name: unigate-ratelimit
       config:
         service_url: http://unigate:8080/v1/check
         rule_id: login-brute-force
         gateway_name: kong
         key_parts: ["ip", "consumer_username"]
         api_key: change-me-kong
   ```

3. Reload/restart Kong. Verify with `kong config parse` before rollout.
