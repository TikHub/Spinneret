--[[
breaker_eval.lua (owner: internal/breaker) — sums the sliding window of an
endpoint group and runs the automatic breaker state machine (spec §6.7, §5.5).

KEYS[1]  P:T:meta
ARGV[1]  endpoint group hkey
ARGV[2]  now (ms)
ARGV[3]  bucket length (ms)
ARGV[4]  number of buckets in the window (1..60)
ARGV[5]  min_requests
ARGV[6]  trip.risk_ratio_gte (0 disables)
ARGV[7]  trip.distinct_captcha_identities_gte (0 disables)
ARGV[8]  trip.success_ratio_lte (0 disables)
ARGV[9]  open_duration (ms)
ARGV[10] max_open_duration (ms)
ARGV[11] reset_open_count_after (ms)
ARGV[12] half_open.close_min_samples
ARGV[13] half_open.close_success_ratio_gte
ARGV[14] mode: "eval" (may transition) | "read" (never writes the breaker)
ARGV[15] enabled: "1" | "0" (breaker policy enabled)

Window: buckets cur-N+1..cur of "win:<eg>:<b>" (t, s, r) and PFCOUNT over the
matching "winh:<eg>:<b>" HyperLogLogs, where cur = floor(now / bucket). Buckets
that started before the last close (lc) are ignored, so a breaker that just
closed is not re-tripped by samples from before it closed.

Transitions (mode "eval", enabled):
  closed    -> open       samples >= min_requests and any enabled condition trips
                          oc = (lc > 0 and now - lc >= reset_after) ? 1 : oc + 1
                          ou = now + min(open * 2^(oc-1), max); lo = now; man = 0
  open      -> half_open  ou > 0 and now >= ou (auto opens, and manual opens with
                          a duration); hw = now, hc = ps = pk = 0, ou = 0. Manual
                          opens with ou = 0 (indefinite) never leave open automatically.
  half_open -> closed     ps >= close_min_samples and pk/ps >= ratio; lc = now, man = 0
  half_open -> open       ps >= close_min_samples and pk/ps < ratio; oc + 1, doubled duration
Transitions (mode "eval", disabled): no trips; automatic open/half_open states
and expired manual opens close; unexpired manual opens are kept.
Every transition increments v. Mode "eval" keeps the "brko" set consistent
with the resulting state on every call (SADD when not closed, SREM when closed).

Lazy half-open detection: acquire.lua half-opens expired breakers itself
without clearing ou, while this script clears ou whenever it half-opens. A
half_open breaker with ou > 0 was therefore half-opened by acquire.lua and not
recorded yet; mode "eval" only reports it (lazy = 1, lazy_at = hw) and clears
ou (v is not incremented again). The probes are evaluated by the next call, so
the returned state is exactly the half-open state to record.

Returns an array of strings:
  {from, to ("" = none), trigger ("auto" | "probe" | ""), t, s, r, captcha,
   st, ou, oc, lo, lc, man, hw, hc, ps, pk, rsn, v, ps_before, pk_before,
   lazy ("1" | "0"), lazy_at}
where st..v is the state after the evaluation and ps_before/pk_before are the
probe statistics the evaluation was based on.
]]

local base = sp_base()
local eg = ARGV[1]
local now = sp_num(ARGV[2], 0)
local bucket_ms = sp_num(ARGV[3], 5000)
local buckets = sp_num(ARGV[4], 12)
local min_requests = sp_num(ARGV[5], 1)
local risk_gte = sp_num(ARGV[6], 0)
local captcha_gte = sp_num(ARGV[7], 0)
local success_lte = sp_num(ARGV[8], 0)
local open_ms = sp_num(ARGV[9], 120000)
local max_open_ms = sp_num(ARGV[10], 3600000)
local reset_after_ms = sp_num(ARGV[11], 1800000)
local close_min = sp_num(ARGV[12], 5)
local close_ratio = sp_num(ARGV[13], 0.8)
local mode = ARGV[14] or 'eval'
local enabled = ARGV[15] ~= '0'

if bucket_ms < 1 then bucket_ms = 1 end
if buckets < 1 then buckets = 1 elseif buckets > 60 then buckets = 60 end
if min_requests < 1 then min_requests = 1 end
if close_min < 1 then close_min = 1 end
if open_ms < 1 then open_ms = 1 end
if max_open_ms < open_ms then max_open_ms = open_ms end

local bkey = base .. 'brk:' .. eg
local okey = base .. 'brko'
local cur = sp_hmget_map(bkey, {'st', 'ou', 'oc', 'lo', 'lc', 'man', 'hw', 'hc', 'ps', 'pk', 'rsn', 'v'})

local st = cur.st
if st ~= 'open' and st ~= 'half_open' then st = 'closed' end
local ou = sp_num(cur.ou, 0)
local oc = sp_num(cur.oc, 0)
local lo = sp_num(cur.lo, 0)
local lc = sp_num(cur.lc, 0)
local man = sp_num(cur.man, 0)
local hw = sp_num(cur.hw, 0)
local hc = sp_num(cur.hc, 0)
local ps = sp_num(cur.ps, 0)
local pk = sp_num(cur.pk, 0)
local rsn = cur.rsn or ''
local v = sp_num(cur.v, 0)
local ps_before, pk_before = ps, pk
local lazy, lazy_at = 0, 0
if mode == 'eval' and st == 'half_open' and ou > 0 then
  lazy = 1
  lazy_at = hw
  if lazy_at <= 0 then lazy_at = now end
  ou = 0
end

-- Sliding window.
local last = math.floor(now / bucket_ms)
local first = last - buckets + 1
if lc > 0 then
  local after_close = math.ceil(lc / bucket_ms)
  if after_close > first then first = after_close end
end
local total, success, risk = 0, 0, 0
local hlls = {}
for b = first, last do
  local suffix = eg .. ':' .. sp_int_str(b)
  local vals = redis.call('HMGET', base .. 'win:' .. suffix, 't', 's', 'r')
  total = total + sp_num(vals[1], 0)
  success = success + sp_num(vals[2], 0)
  risk = risk + sp_num(vals[3], 0)
  hlls[#hlls + 1] = base .. 'winh:' .. suffix
end
local captcha = 0
if #hlls > 0 then
  captcha = redis.call('PFCOUNT', unpack(hlls))
end

-- open * 2^(n-1) capped at max_open_ms (loop avoids overflow for large n).
local function open_for(n)
  local d = open_ms
  local k = 1
  while k < n and d < max_open_ms do
    d = d * 2
    k = k + 1
  end
  if d > max_open_ms then d = max_open_ms end
  return d
end

local function ratio_str(x)
  return string.format('%.2f', x)
end

local from, to, trigger = st, '', ''

local function go_closed(reason, trig)
  to, trigger = 'closed', trig
  st, ou, man, lc = 'closed', 0, 0, now
  hw, hc, ps, pk = 0, 0, 0, 0
  rsn = reason
  v = v + 1
end

local function go_open(reason, trig, next_oc)
  to, trigger = 'open', trig
  oc = next_oc
  st, man, lo = 'open', 0, now
  ou = now + open_for(oc)
  hw, hc, ps, pk = 0, 0, 0, 0
  rsn = reason
  v = v + 1
end

if mode == 'eval' then
  if lazy == 1 then
    -- Only record the lazy half-open transition (see the header).
  elseif not enabled then
    if st == 'half_open' or (st == 'open' and man ~= 1) then
      go_closed('breaker disabled by policy', 'auto')
    elseif st == 'open' and ou > 0 and now >= ou then
      go_closed('manual open expired (breaker disabled by policy)', 'auto')
    end
  elseif st == 'closed' then
    if total >= min_requests and total > 0 then
      local reasons = {}
      local rr = risk / total
      local sr = success / total
      if risk_gte > 0 and rr >= risk_gte then
        reasons[#reasons + 1] = 'risk_ratio ' .. ratio_str(rr) .. ' >= ' .. ratio_str(risk_gte)
      end
      if captcha_gte > 0 and captcha >= captcha_gte then
        reasons[#reasons + 1] = 'captcha_identities ' .. sp_int_str(captcha) .. ' >= ' .. sp_int_str(captcha_gte)
      end
      if success_lte > 0 and sr <= success_lte then
        reasons[#reasons + 1] = 'success_ratio ' .. ratio_str(sr) .. ' <= ' .. ratio_str(success_lte)
      end
      if #reasons > 0 then
        local next_oc = oc + 1
        if lc > 0 and now - lc >= reset_after_ms then
          next_oc = 1
        end
        go_open(table.concat(reasons, '; '), 'auto', next_oc)
      end
    end
  elseif st == 'open' then
    -- An automatic open without an end is corrupt; treat it as expired.
    if (ou > 0 and now >= ou) or (man ~= 1 and ou <= 0) then
      to, trigger = 'half_open', 'auto'
      st, ou = 'half_open', 0
      hw, hc, ps, pk = now, 0, 0, 0
      v = v + 1
    end
  elseif st == 'half_open' then
    if ps >= close_min then
      local pr = pk / ps
      if pr >= close_ratio then
        go_closed('probe success_ratio ' .. ratio_str(pr) .. ' >= ' .. ratio_str(close_ratio), 'probe')
      else
        go_open('probe success_ratio ' .. ratio_str(pr) .. ' < ' .. ratio_str(close_ratio), 'probe', oc + 1)
      end
    end
  end

  if to ~= '' or lazy == 1 then
    redis.call('HSET', bkey,
      'st', st, 'ou', sp_int_str(ou), 'oc', sp_int_str(oc), 'lo', sp_int_str(lo), 'lc', sp_int_str(lc),
      'man', sp_int_str(man), 'hw', sp_int_str(hw), 'hc', sp_int_str(hc), 'ps', sp_int_str(ps),
      'pk', sp_int_str(pk), 'rsn', rsn, 'v', sp_int_str(v))
  end

  -- Keep "brko" consistent with the state, also healing members left behind
  -- by writers that changed the hash without maintaining the set (a stale
  -- member would otherwise make the group a sweep candidate forever).
  if st == 'closed' then
    redis.call('SREM', okey, eg)
  else
    redis.call('SADD', okey, eg)
  end
end

return {
  from, to, trigger,
  sp_int_str(total), sp_int_str(success), sp_int_str(risk), sp_int_str(captcha),
  st, sp_int_str(ou), sp_int_str(oc), sp_int_str(lo), sp_int_str(lc), sp_int_str(man),
  sp_int_str(hw), sp_int_str(hc), sp_int_str(ps), sp_int_str(pk), rsn, sp_int_str(v),
  sp_int_str(ps_before), sp_int_str(pk_before),
  sp_int_str(lazy), sp_int_str(lazy_at),
}
