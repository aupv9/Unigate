-- Atomic multi-window sliding-window-log rate limiter (FR2, FR4, NFR4).
--
-- KEYS[1..N]   one ZSET per window, all sharing a cluster hash tag
-- ARGV[1]      now_ms
-- ARGV[2]      cost (positive integer, number of "hits" this request counts as)
-- ARGV[3]      number of windows N
-- ARGV[4..]    N triples: period_ms, limit, key_ttl_ms (key_ttl_ms just used to EXPIRE)
--
-- Returns a flat array:
--   [allowed(0/1), blocked_index(0 = none, else 1-based window index),
--    retry_after_ms,
--    then for each window: remaining, limit, reset_ms]
local now_ms = tonumber(ARGV[1])
local cost = tonumber(ARGV[2])
local n = tonumber(ARGV[3])

local periods = {}
local limits = {}
local ttls = {}
for i = 1, n do
  periods[i] = tonumber(ARGV[3 + (i - 1) * 3 + 1])
  limits[i] = tonumber(ARGV[3 + (i - 1) * 3 + 2])
  ttls[i] = tonumber(ARGV[3 + (i - 1) * 3 + 3])
end

local counts = {}
local oldest = {}

-- Phase 1: trim expired entries and count current occupancy for every
-- window before deciding anything, so a block on window 2 never leaves
-- window 1 partially incremented.
for i = 1, n do
  local key = KEYS[i]
  local window_start = now_ms - periods[i]
  redis.call('ZREMRANGEBYSCORE', key, '-inf', window_start)
  counts[i] = redis.call('ZCARD', key)
  local oldest_entry = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
  if oldest_entry[2] then
    oldest[i] = tonumber(oldest_entry[2])
  else
    oldest[i] = now_ms
  end
end

local allowed = 1
local blocked_index = 0
local retry_after_ms = 0

for i = 1, n do
  if counts[i] + cost > limits[i] then
    allowed = 0
    blocked_index = i
    local retry = (oldest[i] + periods[i]) - now_ms
    if retry < 0 then retry = 0 end
    if retry > retry_after_ms then retry_after_ms = retry end
  end
end

if allowed == 1 then
  for i = 1, n do
    local key = KEYS[i]
    for c = 1, cost do
      -- unique member per unit so ZCARD reflects total weight consumed
      redis.call('ZADD', key, now_ms, now_ms .. '-' .. c .. '-' .. math.random(1, 1000000000))
    end
    redis.call('PEXPIRE', key, ttls[i])
    counts[i] = counts[i] + cost
  end
end

local result = { allowed, blocked_index, retry_after_ms }
for i = 1, n do
  local remaining = limits[i] - counts[i]
  if remaining < 0 then remaining = 0 end
  table.insert(result, remaining)
  table.insert(result, limits[i])
  table.insert(result, now_ms + periods[i])
end

return result
