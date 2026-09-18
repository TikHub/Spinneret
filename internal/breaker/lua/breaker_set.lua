--[[
breaker_set.lua (owner: internal/breaker) — manual breaker operations (spec §5.5).

KEYS[1]  P:T:meta
ARGV[1]  endpoint group hkey
ARGV[2]  now (ms)
ARGV[3]  operation: "open" | "close"
ARGV[4]  open duration (ms); 0 = indefinite (open only)
ARGV[5]  reason

open:  st=open man=1 ou=(duration > 0 ? now+duration : 0) lo=now hw=hc=ps=pk=0
       rsn v++ SADD brko (oc is kept). Re-opening an open breaker replaces its
       end and reason.
close: st=closed man=0 ou=0 lc=now hw=hc=ps=pk=0 rsn v++ SREM brko (oc is
       kept). Closing a closed breaker changes nothing.

A half_open breaker with ou > 0 was half-opened lazily by acquire.lua and not
recorded yet (see breaker_eval.lua); it is reported with lazy = 1, lazy_at = hw
and the reason and manual flag it had, so the caller can record that
transition before the manual one.

Returns an array of strings:
  {from, to, changed ("1" | "0"), st, ou, oc, lo, lc, man, hw, hc, ps, pk, rsn, v,
   lazy ("1" | "0"), lazy_at, rsn_before, man_before}
]]

local base = sp_base()
local eg = ARGV[1]
local now = sp_num(ARGV[2], 0)
local op = ARGV[3]
local duration = sp_num(ARGV[4], 0)
local reason = ARGV[5] or ''

if op ~= 'open' and op ~= 'close' then
  return redis.error_reply('breaker_set: unknown operation')
end

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

local from = st
local changed = '0'
local rsn_before, man_before = rsn, man
local lazy, lazy_at = 0, 0
if st == 'half_open' and ou > 0 then
  lazy = 1
  lazy_at = hw
  if lazy_at <= 0 then lazy_at = now end
end

if op == 'open' then
  st, man, lo = 'open', 1, now
  if duration > 0 then
    ou = now + duration
  else
    ou = 0
  end
  hw, hc, ps, pk = 0, 0, 0, 0
  rsn = reason
  v = v + 1
  changed = '1'
  redis.call('SADD', okey, eg)
elseif st ~= 'closed' then
  st, man, ou, lc = 'closed', 0, 0, now
  hw, hc, ps, pk = 0, 0, 0, 0
  rsn = reason
  v = v + 1
  changed = '1'
  redis.call('SREM', okey, eg)
end

if changed == '1' then
  redis.call('HSET', bkey,
    'st', st, 'ou', sp_int_str(ou), 'oc', sp_int_str(oc), 'lo', sp_int_str(lo), 'lc', sp_int_str(lc),
    'man', sp_int_str(man), 'hw', sp_int_str(hw), 'hc', sp_int_str(hc), 'ps', sp_int_str(ps),
    'pk', sp_int_str(pk), 'rsn', rsn, 'v', sp_int_str(v))
end

return {
  from, st, changed,
  st, sp_int_str(ou), sp_int_str(oc), sp_int_str(lo), sp_int_str(lc), sp_int_str(man),
  sp_int_str(hw), sp_int_str(hc), sp_int_str(ps), sp_int_str(pk), rsn, sp_int_str(v),
  sp_int_str(lazy), sp_int_str(lazy_at), rsn_before, sp_int_str(man_before),
}
