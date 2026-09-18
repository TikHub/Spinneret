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
  10 reuse anchor a|r          11 reuse scope e|s           12 probe weight factor ppm
  13 probe max leases          14 warmup duration ms        15 warmup quota factor ppm
  16 health baseline           17 health tau ms             18 half-open probes per 10 s
  19 normalized session key    20 sticky ttl ms (0 = off)   21 late report window ms (unused:
                                                                active lease hashes have no TTL)
  22 report shards             23 node                      24 token id
  25 namespace id              26 proxy mode n|p|b|r        27 proxy region match 0|1
  28 rebind tolerance ms       29 max rebinds per day       30 today YYYYMMDD (UTC)
  31.. n kinds, kinds...; n providers, providers...; n regions, regions...;
       n tags, tags...; n quotas, (limit, window ms)...; count lease id prefixes;
       random values in [0, 1000000) (count*4+16 are supplied; further values are
       derived). Fractions travel as parts per million because Lua 5.1 parses a
       decimal numeral of ten digits or more, and any numeral with a fractional
       part, about eight times slower than a short integer (spec §5.7).

Returns
  {"OK", transition, n, <14 fields per lease>...} with lease fields
    lease id, identity id, type name, payload version, type version, expires ms,
    breaker probe 0|1, sticky 0|1, binding 0|b (first binding)|r (rebind), proxy hkey, proxy id,
    identity state, identity hkey, rebinds today
  {"EXHAUSTED" | "BREAKER_OPEN" | "NO_PROXY", transition, retry after ms}
transition is "1" when this call moved the breaker from open to half_open.
]]

local base = sp_base()
-- sp_dnum is tonumber for the millisecond timestamps of the hot path without
-- the slow C path Lua 5.1 takes from ten digits on.
local now = sp_dnum(ARGV[1])
local eg = ARGV[3]

-- Configuration of this call. These are chunk-level locals rather than fields
-- of one table: acquire reads them about a hundred times per call, and the
-- table cost 0.6 us to build and a hash lookup per read. Names are the ARGV
-- names above; "late" (ARGV 21) is not read by this script at all.
local count = tonumber(ARGV[2])
local strategy = ARGV[4]
local ksample = tonumber(ARGV[5])
local ttl = tonumber(ARGV[6])
local lifetime = tonumber(ARGV[7])
local mc = tonumber(ARGV[8])
local ri = tonumber(ARGV[9])
local ra = ARGV[10]
local rs = ARGV[11]
local probe_wf = tonumber(ARGV[12]) / 1000000
local probe_max = tonumber(ARGV[13])
local warmup = tonumber(ARGV[14])
local warmup_factor = tonumber(ARGV[15]) / 1000000
local baseline = tonumber(ARGV[16])
local tau = tonumber(ARGV[17])
local probe_limit = tonumber(ARGV[18])
local session = ARGV[19]
local sticky_ttl = tonumber(ARGV[20])
local shards = tonumber(ARGV[22])
local node = ARGV[23]
local token = ARGV[24]
local ns = ARGV[25]
local pmode = ARGV[26]
local pregion = ARGV[27] == '1'
local tolerance = tonumber(ARGV[28])
local max_rebinds = tonumber(ARGV[29])
local today = ARGV[30]
local kinds, providers, regions, tags
local nq, qlim, qwin, qwstr, qfields
local qmax = 0
local prefixes = {}

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

-- The proxy filter sets, the tag list and the quota tables stay nil when the
-- policy has none of them: an empty Lua table still costs an allocation and a
-- collection, and the dominant configuration (no proxies, no quotas) creates
-- six of them per call for nothing. Their readers test for nil.
kinds = read_set()
providers = read_set()
regions = read_set()
local ntags = tonumber(next_arg())
if ntags > 0 then
  tags = {}
  for j = 1, ntags do
    tags[j] = ',' .. next_arg() .. ','
  end
end
nq = tonumber(next_arg())
if nq > 0 then
  qlim, qwin, qwstr, qfields = {}, {}, {}, {}
  for q = 1, nq do
    qlim[q] = tonumber(next_arg())
    qwstr[q] = next_arg()
    qwin[q] = tonumber(qwstr[q])
    if qwin[q] > qmax then
      qmax = qwin[q]
    end
    qfields[3 * q - 2] = qwstr[q] .. ':c'
    qfields[3 * q - 1] = qwstr[q] .. ':n'
    qfields[3 * q] = qwstr[q] .. ':p'
  end
end
for j = 1, count do
  prefixes[j] = next_arg()
end
local rand_base = pos - 1
local rand_n = #ARGV - rand_base

-- rnd returns the next Go-supplied random value in [0, 1) (converted lazily
-- from its parts-per-million integer); once they are used up it derives
-- further values deterministically from them.
local ridx = 0
local function rnd()
  ridx = ridx + 1
  if rand_n <= 0 then
    return (ridx * 0.6180339887498949) % 1
  end
  local v = (tonumber(ARGV[rand_base + ((ridx - 1) % rand_n) + 1]) or 500000) / 1000000
  if ridx > rand_n then
    v = (v * 7919 + ridx * 0.6180339887498949) % 1
  end
  return v
end

local rdy = base .. 'rdy:' .. eg
local hskey = base .. 'hs:' .. eg
local brkkey = base .. 'brk:' .. eg
local pxrdy = base .. 'pxrdy'
local short_push = now + math.min(ttl, 5000)
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

-- Helpers only some configurations need. They are declared here and assigned
-- by the parts below inside the "if" that decides whether the configuration
-- can call them at all, because creating a chunk-level closure costs about
-- 0.2 us per script call: a site without proxies and without quotas must not
-- pay for the four it never calls.
local select_proxy, bind_check, quota_push, quota_write
