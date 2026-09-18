--[[
lease_retain.lua (signal): look up the lease of accepted reports and keep its
hash readable until the worker can process them.

KEYS[1]  P:T:ls:<leaseId>   lease hash (site slot)
ARGV[1]  expected namespace id (that of the lease site)
ARGV[2]  now ms
ARGV[3]  retention ms: how long after ingest the hash must stay readable
ARGV[4]  number of reports ingested for the lease in this request
ARGV[5]  late report window ms

When the hash exists and belongs to the expected namespace:
  * "rv" += ARGV[4] (reports ingested; the reaper does not count such leases
    as abandoned even when the worker has not processed the reports yet);
  * only when the reports are timely (the lease is active, or it ended at most
    the late report window ago; the worker judges lateness the same way):
      - "kx" = max(kx, now + retention): ending the lease keeps the hash for
        max(late window, kx - now) (scheduler lease_end.lua);
      - an ended lease (the hash has a TTL) gets its TTL raised to the
        retention. An active lease has no TTL and keeps none.
    Late reports never extend the retention, so an ended lease hash lives at
    most late window + retention after the lease ended.
A missing hash is not created.

Returns the namespace id of the lease ("" when the hash does not exist).
]]

local key = KEYS[1]
local v = redis.call('HMGET', key, 'ns', 'kx', 'st', 'end')
if not v[1] then
  return ''
end
if v[1] ~= ARGV[1] then
  return v[1]
end
local now = tonumber(ARGV[2])
local retain = tonumber(ARGV[3])
local late = tonumber(ARGV[5])
redis.call('HINCRBY', key, 'rv', ARGV[4])
-- An ended lease always carries "end"; a hash without it is treated as
-- timely so that a report is never denied retention by a partial record.
local ended = sp_num(v[4], 0)
if v[3] ~= 'active' and ended > 0 and now - ended > late then
  return v[1]
end
local kx = now + retain
if kx > sp_num(v[2], 0) then
  redis.call('HSET', key, 'kx', sp_int_str(kx))
end
local ttl = redis.call('PTTL', key)
if ttl >= 0 and ttl < retain then
  redis.call('PEXPIRE', key, sp_int_str(retain))
end
return v[1]
