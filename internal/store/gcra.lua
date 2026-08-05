-- Atomic GCRA (Generic Cell Rate Algorithm) limiter (FR4): smooths
-- bursts while still enforcing an average rate, unlike a hard fixed
-- window.
--
-- KEYS[1]   the bucket's TAT (theoretical arrival time) key
-- ARGV[1]   now_ms
-- ARGV[2]   cost
-- ARGV[3]   period_ms   (the window over which `limit` requests are allowed)
-- ARGV[4]   limit       (steady-state requests per period_ms)
-- ARGV[5]   burst       (extra requests allowed to land immediately)
--
-- Returns: [allowed(0/1), remaining, retry_after_ms, reset_ms]
local now_ms = tonumber(ARGV[1])
local cost = tonumber(ARGV[2])
local period_ms = tonumber(ARGV[3])
local limit = tonumber(ARGV[4])
local burst = tonumber(ARGV[5])

local emission_interval = period_ms / limit
local burst_offset = emission_interval * burst

local tat = tonumber(redis.call('GET', KEYS[1]))
if tat == nil or tat < now_ms then
  tat = now_ms
end

local increment = emission_interval * cost
local new_tat = tat + increment
local allow_at = new_tat - burst_offset

local allowed
local retry_after_ms = 0
local remaining

if now_ms < allow_at then
  allowed = 0
  retry_after_ms = allow_at - now_ms
  -- do not consume the bucket on a rejected request
  local current_tat = tat
  local occupied = current_tat - now_ms
  remaining = math.floor((burst_offset - occupied) / emission_interval)
  if remaining < 0 then remaining = 0 end
else
  allowed = 1
  local ttl_ms = math.ceil(new_tat - now_ms + burst_offset)
  if ttl_ms < 1 then ttl_ms = 1 end
  redis.call('SET', KEYS[1], new_tat, 'PX', ttl_ms)
  local occupied = new_tat - now_ms
  remaining = math.floor((burst_offset - occupied) / emission_interval)
  if remaining < 0 then remaining = 0 end
end

local reset_ms = now_ms + math.max(retry_after_ms, emission_interval)

return { allowed, remaining, math.ceil(retry_after_ms), math.ceil(reset_ms) }
