--[[
acquire.lua (owner: internal/scheduler) - spec §6.1 steps 1-7.

The script body is split by concern and concatenated in this order by
internal/scheduler/scripts.go: acquire.lua (arguments and shared state),
acquire_filters.lua, acquire_proxy.lua, acquire_write.lua, acquire_select.lua,
acquire_main.lua.

KEYS[1] = "P:T:meta" of the site.

ARGV (positions mirror internal/scheduler/args.go):
   1 now ms                     2 count (1..50)              3 endpoint group hkey
   4 strategy w|l|r|b           5 candidate sample K         6 lease ttl ms
   7 max lease lifetime ms      8 max concurrent leases      9 reuse interval ms
  10 reuse anchor a|r          11 reuse scope e|s           12 probe weight factor
  13 probe max leases          14 warmup duration ms        15 warmup quota factor
  16 health baseline           17 health tau ms             18 half-open probes per 10 s
  19 normalized session key    20 sticky ttl ms (0 = off)   21 late report window ms (unused:
                                                                active lease hashes have no TTL)
  22 report shards             23 node                      24 token id
  25 namespace id              26 proxy mode n|p|b|r        27 proxy region match 0|1
  28 rebind tolerance ms       29 max rebinds per day       30 today YYYYMMDD (UTC)
  31.. n kinds, kinds...; n providers, providers...; n regions, regions...;
       n tags, tags...; n quotas, (limit, window ms)...; count lease id prefixes;
       random floats in [0,1) (count*4+16 are supplied; further values are derived).

Returns
  {"OK", transition, n, <14 fields per lease>...} with lease fields
    lease id, identity id, type name, payload version, type version, expires ms,
    breaker probe 0|1, sticky 0|1, binding 0|b (first binding)|r (rebind), proxy hkey, proxy id,
    identity state, identity hkey, rebinds today
  {"EXHAUSTED" | "BREAKER_OPEN" | "NO_PROXY", transition, retry after ms}
transition is "1" when this call moved the breaker from open to half_open.
]]

local base = sp_base()
local now = tonumber(ARGV[1])
local eg = ARGV[3]

local C = {
  count = tonumber(ARGV[2]),
  strategy = ARGV[4],
  k = tonumber(ARGV[5]),
  ttl = tonumber(ARGV[6]),
  lifetime = tonumber(ARGV[7]),
  mc = tonumber(ARGV[8]),
  ri = tonumber(ARGV[9]),
  ra = ARGV[10],
  rs = ARGV[11],
  probe_wf = tonumber(ARGV[12]),
  probe_max = tonumber(ARGV[13]),
  warmup = tonumber(ARGV[14]),
  warmup_factor = tonumber(ARGV[15]),
  baseline = tonumber(ARGV[16]),
  tau = tonumber(ARGV[17]),
  probe_limit = tonumber(ARGV[18]),
  session = ARGV[19],
  sticky_ttl = tonumber(ARGV[20]),
  late = tonumber(ARGV[21]),
  shards = tonumber(ARGV[22]),
  node = ARGV[23],
  token = ARGV[24],
  ns = ARGV[25],
  pmode = ARGV[26],
  pregion = ARGV[27] == '1',
  tolerance = tonumber(ARGV[28]),
  max_rebinds = tonumber(ARGV[29]),
  today = ARGV[30],
}

local pos = 31
local function next_arg()
  local v = ARGV[pos]
  pos = pos + 1
  return v
end

local function read_set()
  local n = tonumber(next_arg())
  if n == 0 then
    return nil
  end
  local s = {}
  for _ = 1, n do
    s[next_arg()] = true
  end
  return s
end

C.kinds = read_set()
C.providers = read_set()
C.regions = read_set()
C.tags = {}
for j = 1, tonumber(next_arg()) do
  C.tags[j] = ',' .. next_arg() .. ','
end
C.nq = tonumber(next_arg())
C.qlim, C.qwin, C.qwstr, C.qfields = {}, {}, {}, {}
C.qmax = 0
for q = 1, C.nq do
  C.qlim[q] = tonumber(next_arg())
  C.qwstr[q] = next_arg()
  C.qwin[q] = tonumber(C.qwstr[q])
  if C.qwin[q] > C.qmax then
    C.qmax = C.qwin[q]
  end
  C.qfields[3 * q - 2] = C.qwstr[q] .. ':c'
  C.qfields[3 * q - 1] = C.qwstr[q] .. ':n'
  C.qfields[3 * q] = C.qwstr[q] .. ':p'
end
C.prefixes = {}
for j = 1, C.count do
  C.prefixes[j] = next_arg()
end
local rand_base = pos - 1
local rand_n = #ARGV - rand_base

-- rnd returns the next Go-supplied random float (converted lazily); once they
-- are used up it derives further values deterministically from them.
local ridx = 0
local function rnd()
  ridx = ridx + 1
  if rand_n <= 0 then
    return (ridx * 0.6180339887498949) % 1
  end
  local v = tonumber(ARGV[rand_base + ((ridx - 1) % rand_n) + 1]) or 0.5
  if ridx > rand_n then
    v = (v * 7919 + ridx * 0.6180339887498949) % 1
  end
  return v
end

local rdy = base .. 'rdy:' .. eg
local hskey = base .. 'hs:' .. eg
local brkkey = base .. 'brk:' .. eg
local pxrdy = base .. 'pxrdy'
local short_push = now + math.min(C.ttl, 5000)
local PROBE_WINDOW = 10000
local LONG_PUSH = 600000

-- Shared mutable state of this call.
local transition = '0' -- "1" once the breaker moved from open to half_open
local probe = false    -- leases issued by this call are breaker probes
local picked = 0       -- leases written so far
local stkkey = nil     -- sticky session key, when stickiness applies
local rr_cursor = nil  -- round-robin cursor (loaded lazily)
local skip, nskip = {}, 0 -- identities already taken by this call
-- push_xl: the last "push" verdict of evaluate() was (also) caused by another
-- group's exclusive lease on the identity, so the score written to rdy:<eg>
-- has to be restored when that lease ends (lease_end.lua, field "xg").
local push_xl = false
local out = {'OK', '0', '0'}
