--[[
lease_retain.lua (signal): look up the leases of accepted reports and keep
their hashes readable until the worker can process them.

KEYS[1..N]  P:T:ls:<leaseId>   lease hashes, all of one site (one hash tag, so
                               the call is atomic and single-slot under Redis
                               Cluster)
ARGV[1]  expected namespace id (that of the lease site)
ARGV[2]  now ms
ARGV[3]  retention ms: how long after ingest a hash must stay readable
ARGV[4]  late report window ms
ARGV[5..N+4] number of reports ingested for lease k in this request (ARGV[k+4])

One call carries every lease of a site instead of one call per lease: a report
batch of 100 against 100 distinct leases paid 100 EVALSHA dispatches and 100
preludes, about 4 us per lease of pure overhead.

For each key, when the hash exists and belongs to the expected namespace:
  * "rv" += the report count (the reaper does not count such leases as
    abandoned even when the worker has not processed the reports yet);
  * only when the reports are timely (the lease is active, or it ended at most
    the late report window ago; the worker judges lateness the same way):
      - "kx" = max(kx, now + retention): ending the lease keeps the hash for
        max(late window, kx - now) (scheduler lease_end.lua);
      - the hash TTL is raised to the retention with PEXPIRE GT, which leaves a
        key without a TTL persistent: an active lease has none and keeps none,
        and an ended one is only ever extended.
    Late reports never extend the retention, so an ended lease hash lives at
    most late window + retention after the lease ended.
A missing hash is not created.

Returns an array of N namespace ids ("" where the hash does not exist).
]]

local ns_want = ARGV[1]
local now = sp_num(ARGV[2], 0)
local retain = tonumber(ARGV[3]) or 0
local late = tonumber(ARGV[4]) or 0
local out = {}
for k = 1, #KEYS do
  local key = KEYS[k]
  local v = redis.call('HMGET', key, 'ns', 'kx', 'st', 'end')
  local ns = v[1]
  if not ns then
    out[k] = ''
  else
    out[k] = ns
    if ns == ns_want then
      redis.call('HINCRBY', key, 'rv', ARGV[k + 4])
      -- An ended lease always carries "end"; a hash without it is treated as
      -- timely so that a report is never denied retention by a partial record.
      local timely = true
      if v[3] ~= 'active' then
        local ended = sp_num(v[4], 0)
        timely = ended <= 0 or now - ended <= late
      end
      if timely then
        local kx = now + retain
        if kx > sp_num(v[2], 0) then
          redis.call('HSET', key, 'kx', kx)
        end
        if v[3] ~= 'active' then
          -- Only an ended hash carries a TTL (spec §5.3), so an active lease
          -- needs no call at all; "GT" then raises a TTL below the retention
          -- and leaves a longer one alone, which is what the PTTL read of
          -- earlier releases decided in Lua.
          redis.call('PEXPIRE', key, retain, 'GT')
        end
      end
    end
  end
end
return out
