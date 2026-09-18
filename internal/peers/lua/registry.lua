--[[
registry.lua (peers): API instance heartbeat and live-member count.

KEYS[1] P:acquirers  ZSET instance id -> heartbeat ms
ARGV[1] instance id
ARGV[2] live window ms

Timestamps come from the Redis server clock (TIME) so skewed instances agree
on liveness. The heartbeat is recorded, members whose heartbeat is older than
the live window are removed, and the number of members inside the window -
including this one - is returned.
]]

local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local cutoff = now - tonumber(ARGV[2])
redis.call('ZADD', KEYS[1], sp_score_str(now), ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', '(' .. sp_score_str(cutoff))
return redis.call('ZCOUNT', KEYS[1], sp_score_str(cutoff), '+inf')
