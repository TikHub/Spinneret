--[[
sync_proxies.lua (owner: hotstate)

Materializes proxies into one site (spec §5.4): writes the static fields of
"px:<p>" and the proxy ready queue "pxrdy". Health and lease fields written by
other scripts (al sc sts sn nf lf cd) are preserved.

"bu" (ban until, -1 permanent) and "qu" (quarantine until) mirror the lifecycle
end times that apply.lua compares against before applying an automatic proxy
ban or quarantine, so a manual (PostgreSQL-first) ban or quarantine is never
shortened by a later automatic one. PostgreSQL stores both in
proxies.ban_until.

"sct" is the change time (ms) of the lifecycle fields st/bu/qu: apply.lua
writes it with every Redis-first change and this script writes
proxies.state_changed_at whenever it applies the PostgreSQL lifecycle fields.
In authoritative mode the PostgreSQL lifecycle fields win unless Redis holds a
newer change (sct greater than state_changed_at) that is still in force (a
ban or quarantine whose end has passed never outlives an older PostgreSQL
state), so an automatic ban the StateWriter has not persisted yet is not
reverted by a stale row.

"gcd" (global cooldown) is max(Redis, PostgreSQL) in both modes: automatic
proxy-global cooldowns are written by apply.lua to Redis only, so a
synchronization never shortens them. Manual changes that must shorten a
cooldown (proxy operations) overwrite gcd with cooldown.lua after syncing.

KEYS[1] = "P:T:meta"

ARGV[1]  mode: "a" authoritative (st, bu and qu from PostgreSQL unless Redis
         holds a newer change, see above) or "m" merge (st, bu and qu present
         in Redis win)
ARGV[2]  now (ms)
then     proxy records of 13 values each:
           proxy hkey, pid, st, kd, rg, pv, tg (",a,b,"), mc, uv, gcd (ms),
           bu (ms, -1 permanent, 0 none), qu (ms, 0 none), sct (ms)

Returns {synced, ready}.
]]

local base = sp_base()
local merge = ARGV[1] == 'm'
local rk = base .. 'pxrdy'
local REC = 13
local synced, ready = 0, 0
local now = sp_num(ARGV[2], 0)
local pos = 3
local nargs = #ARGV

-- Reports whether the lifecycle fields stored in Redis are kept.
local function keep_redis_lifecycle(st, cur_sct, pg_sct, bu, qu)
  if not st then
    return false
  end
  if merge then
    return true
  end
  if sp_num(cur_sct, 0) <= pg_sct then
    return false
  end
  if st == 'banned' then
    local b = sp_num(bu, 0)
    return b == -1 or b > now
  end
  if st == 'quarantined' then
    return sp_num(qu, 0) > now
  end
  return true
end

while pos + REC - 1 <= nargs do
  local p = ARGV[pos]
  local pk = base .. 'px:' .. p
  local cur = redis.call('HMGET', pk, 'st', 'gcd', 'cd', 'bu', 'qu', 'sct')
  local st = ARGV[pos + 2]
  local gcd = sp_num(ARGV[pos + 9], 0)
  local existing_gcd = sp_num(cur[2], 0)
  if existing_gcd > gcd then gcd = existing_gcd end
  local bu, qu = ARGV[pos + 10], ARGV[pos + 11]
  local sct = sp_num(ARGV[pos + 12], 0)
  if keep_redis_lifecycle(cur[1], cur[6], sct, cur[4], cur[5]) then
    st = cur[1]
    if cur[4] then bu = cur[4] end
    if cur[5] then qu = cur[5] end
    if cur[6] then sct = sp_num(cur[6], 0) end
  end
  redis.call('HSET', pk,
    'pid', ARGV[pos + 1], 'st', st, 'kd', ARGV[pos + 3], 'rg', ARGV[pos + 4], 'pv', ARGV[pos + 5],
    'tg', ARGV[pos + 6], 'mc', ARGV[pos + 7], 'uv', ARGV[pos + 8], 'gcd', sp_int_str(gcd),
    'bu', bu, 'qu', qu, 'sct', sp_int_str(sct))
  if st == 'active' then
    local s = sp_num(cur[3], 0)
    if gcd > s then s = gcd end
    redis.call('ZADD', rk, sp_score_str(s), p)
    ready = ready + 1
  else
    redis.call('ZREM', rk, p)
  end
  synced = synced + 1
  pos = pos + REC
end

return { synced, ready }
