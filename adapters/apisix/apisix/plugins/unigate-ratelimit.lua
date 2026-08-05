-- APISIX adapter for the Unigate rate-limit service (FR6).
--
-- Same contract as the Kong adapter: extract a composite key, call
-- CheckLimit over HTTP, translate allow/block into the APISIX request
-- lifecycle, forwarding the standardized rate-limit headers (FR7).
local core = require("apisix.core")
local http = require("resty.http")
local cjson_safe = require("cjson.safe")

local plugin_name = "unigate-ratelimit"

local schema = {
  type = "object",
  properties = {
    service_url = { type = "string", default = "http://unigate:8080/v1/check" },
    rule_id = { type = "string" },
    namespace = { type = "string" },
    gateway_name = { type = "string", default = "apisix" },
    api_key = { type = "string" },
    key_parts = {
      type = "array",
      minItems = 1,
      default = { "ip" },
      items = { type = "string", pattern = "^(ip|consumer_username|header:.+)$" },
    },
    cost = { type = "integer", default = 1, minimum = 1 },
    timeout_ms = { type = "integer", default = 50, minimum = 1 },
    -- Adapter-level circuit breaker if Unigate itself is unreachable,
    -- independent of the rule's own server-side fail_open/fail_closed.
    fail_open = { type = "boolean", default = true },
  },
  required = { "rule_id" },
}

local _M = {
  version = 0.1,
  priority = 1010,
  name = plugin_name,
  schema = schema,
}

function _M.check_schema(conf)
  return core.schema.check(schema, conf)
end

local function extract_key(conf, ctx)
  local components = {}
  for _, part in ipairs(conf.key_parts) do
    if part == "ip" then
      core.table.insert(components, { kind = "ip", value = core.request.get_remote_client_ip(ctx) })
    elseif part == "consumer_username" then
      local consumer_name = ctx.consumer_name
      if consumer_name then
        core.table.insert(components, { kind = "username", value = consumer_name })
      end
    elseif core.string.has_prefix(part, "header:") then
      local header_name = part:sub(8)
      local value = core.request.header(ctx, header_name)
      if value then
        core.table.insert(components, { kind = header_name, value = value })
      end
    end
  end
  return components
end

local function call_check_limit(conf, key_components)
  local httpc = http.new()
  httpc:set_timeout(conf.timeout_ms)

  local body = core.json.encode({
    rule_id = conf.rule_id,
    key = key_components,
    cost = conf.cost,
    gateway = conf.gateway_name,
    namespace = conf.namespace,
  })

  local headers = { ["Content-Type"] = "application/json" }
  if conf.api_key then
    headers["X-Unigate-Api-Key"] = conf.api_key
    headers["X-Unigate-Gateway"] = conf.gateway_name
  end

  local res, err = httpc:request_uri(conf.service_url, {
    method = "POST",
    body = body,
    headers = headers,
    keepalive_timeout = 60000,
    keepalive_pool = 30,
  })

  if not res then
    return nil, err
  end
  if res.status ~= 200 and res.status ~= 429 then
    return nil, "unigate returned unexpected status " .. tostring(res.status)
  end

  local decoded, decode_err = cjson_safe.decode(res.body)
  if not decoded then
    return nil, "failed to decode unigate response: " .. tostring(decode_err)
  end
  return decoded, nil
end

function _M.access(conf, ctx)
  local key_components = extract_key(conf, ctx)
  if #key_components == 0 then
    core.log.warn("unigate-ratelimit: no key components extracted, skipping check")
    return
  end

  local decision, err = call_check_limit(conf, key_components)
  if not decision then
    core.log.error("unigate-ratelimit: CheckLimit call failed: ", err)
    if conf.fail_open then
      return
    end
    return 503, { message = "rate limit service unavailable" }
  end

  local headers = {}
  if decision.limit then headers["X-RateLimit-Limit"] = tostring(decision.limit) end
  if decision.remaining then headers["X-RateLimit-Remaining"] = tostring(decision.remaining) end
  if decision.reset_seconds then headers["X-RateLimit-Reset"] = tostring(decision.reset_seconds) end

  if not decision.allow then
    headers["Retry-After"] = tostring(decision.retry_after_seconds or 1)
    core.response.set_header(headers)
    local message = "rate limit exceeded"
    if decision.locked_out then
      message = "temporarily locked out due to repeated violations"
    end
    return 429, { message = message }
  end

  core.response.set_header(headers)
end

return _M
