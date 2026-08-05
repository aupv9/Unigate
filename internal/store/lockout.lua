-- Progressive brute-force lockout (FR5): after N consecutive violations,
-- the lockout duration escalates through configured steps
-- (e.g. 1 min -> 5 min -> 30 min).
--
-- KEYS[1]   lockout hash key: fields "violations", "locked_until_ms"
-- ARGV[1]   now_ms
-- ARGV[2]   mode: "check" or "violate"
-- ARGV[3]   violation_ttl_ms (how long a violation stays "consecutive")
-- ARGV[4]   number of steps S
-- ARGV[5..] S pairs: after_violations, lockout_ms (ascending by after_violations)
--
-- Returns: [locked(0/1), locked_until_ms, violations]
local key = KEYS[1]
local now_ms = tonumber(ARGV[1])
local mode = ARGV[2]
local violation_ttl_ms = tonumber(ARGV[3])
local num_steps = tonumber(ARGV[4])

local steps = {}
for i = 1, num_steps do
  local after = tonumber(ARGV[4 + (i - 1) * 2 + 1])
  local dur = tonumber(ARGV[4 + (i - 1) * 2 + 2])
  steps[i] = { after = after, dur = dur }
end

local state = redis.call('HMGET', key, 'violations', 'locked_until_ms')
local violations = tonumber(state[1]) or 0
local locked_until = tonumber(state[2]) or 0

if mode == 'violate' then
  violations = violations + 1
  local lockout_ms = 0
  for i = 1, num_steps do
    if violations >= steps[i].after then
      lockout_ms = steps[i].dur
    end
  end
  if lockout_ms > 0 then
    locked_until = now_ms + lockout_ms
  end
  redis.call('HSET', key, 'violations', violations, 'locked_until_ms', locked_until)
  local ttl_ms = violation_ttl_ms
  if locked_until - now_ms > ttl_ms then
    ttl_ms = locked_until - now_ms
  end
  redis.call('PEXPIRE', key, ttl_ms)
end

local locked = 0
if now_ms < locked_until then
  locked = 1
else
  locked_until = 0
end

return { locked, locked_until, violations }
