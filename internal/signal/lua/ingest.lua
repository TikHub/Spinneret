--[[
ingest.lua (signal): de-duplicate and enqueue the reports of one stream shard.

KEYS[1]     P:R:stream                 report stream of the shard
KEYS[2..N]  P:R:dd:<reportId>          dedup marker of report k = index - 1
ARGV[1]     dedup TTL in milliseconds
ARGV[2]     approximate stream MAXLEN
ARGV[3..N+1] encoded event JSON of report k (ARGV[k + 2])

Every KEYS entry carries the shard hash tag {r<shard>}, so the script is atomic
under Redis Cluster: a report is appended only when its dedup marker was set
(SET NX PX) in the same call, never one without the other.

Returns an array with one integer per report: 1 accepted, 0 duplicated.
]]

local ttl = ARGV[1]
local maxlen = ARGV[2]
local stream = KEYS[1]
local result = {}
for idx = 2, #KEYS do
  local fresh = redis.call('SET', KEYS[idx], '1', 'NX', 'PX', ttl)
  if fresh then
    redis.call('XADD', stream, 'MAXLEN', '~', maxlen, '*', 'v', '1', 'd', ARGV[idx + 1])
    result[idx - 1] = 1
  else
    result[idx - 1] = 0
  end
end
return result
