--[[
registry.lua (worker): worker registry heartbeat and live-member listing.

KEYS[1] P:workers   ZSET instance id -> heartbeat ms
ARGV[1] mode        "beat" (heartbeat + prune + list) | "list" (list only)
ARGV[2] instance id (ignored by "list")
ARGV[3] live window ms

Timestamps come from the Redis server clock (TIME), so instances with skewed
clocks agree on liveness. "beat" records the heartbeat and removes members
whose heartbeat is older than the live window. Both modes return the ids of the
live members (heartbeat within the window); callers sort them.
]]

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local cutoff = now - tonumber(ARGV[3])
if ARGV[1] == 'beat' then
  redis.call('ZADD', KEYS[1], sp_score_str(now), ARGV[2])
  redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', '(' .. sp_score_str(cutoff))
end
return redis.call('ZRANGEBYSCORE', KEYS[1], sp_score_str(cutoff), '+inf')
