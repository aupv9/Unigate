-- Kong adapter for the Unigate rate-limit service (FR6).
--
-- Responsibilities are deliberately narrow: extract the identifier(s)
-- Unigate's rule expects, call CheckLimit over HTTP, and translate the
-- decision into a Kong response (429 + standard headers, or pass
-- through with the same headers attached for the upstream/client).
local http = require "resty.http"
local cjson = require "cjson.safe"

local kong = kong

local UnigateRateLimitHandler = {
  PRIORITY = 910, -- run early, ahead of upstream proxying, alongside/after auth plugins
  VERSION = "0.1.0",
}

local function extract_key(conf)
  local components = {}
  for _, part in ipairs(conf.key_parts) do
    if part == "ip" then
      table.insert(components, { kind = "ip", value = kong.client.get_forwarded_ip() })
    elseif part == "consumer_username" then
      local consumer = kong.client.get_consumer()
      if consumer and consumer.username then
        table.insert(components, { kind = "username", value = consumer.username })
      end
    elseif part:sub(1, 7) == "header:" then
      local header_name = part:sub(8)
      local value = kong.request.get_header(header_name)
      if value then
        table.insert(components, { kind = header_name, value = value })
      end
    end
  end
  return components
end

local function call_check_limit(conf, key_components)
  local client = http.new()
  client:set_timeout(conf.timeout_ms)

  local body = cjson.encode({
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

  local res, err = client:request_uri(conf.service_url, {
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
    return nil, "unigate returned unexpected status " .. tostring(res.status) .. ": " .. tostring(res.body)
  end

  local decoded, decode_err = cjson.decode(res.body)
  if not decoded then
    return nil, "failed to decode unigate response: " .. tostring(decode_err)
  end
  return decoded, nil
end

function UnigateRateLimitHandler:access(conf)
  local key_components = extract_key(conf)
  if #key_components == 0 then
    kong.log.warn("unigate-ratelimit: no key components extracted, skipping check")
    return
  end

  local decision, err = call_check_limit(conf, key_components)
  if not decision then
    kong.log.err("unigate-ratelimit: CheckLimit call failed: ", err)
    if conf.fail_open then
      return -- allow the request through when the rate-limit brain is unreachable
    end
    return kong.response.exit(503, { message = "rate limit service unavailable" })
  end

  if decision.limit then
    kong.response.set_header("X-RateLimit-Limit", tostring(decision.limit))
  end
  if decision.remaining then
    kong.response.set_header("X-RateLimit-Remaining", tostring(decision.remaining))
  end
  if decision.reset_seconds then
    kong.response.set_header("X-RateLimit-Reset", tostring(decision.reset_seconds))
  end

  if not decision.allow then
    local retry_after = decision.retry_after_seconds or 1
    kong.response.set_header("Retry-After", tostring(retry_after))
    local message = "rate limit exceeded"
    if decision.locked_out then
      message = "temporarily locked out due to repeated violations"
    end
    return kong.response.exit(429, { message = message })
  end
end

return UnigateRateLimitHandler
