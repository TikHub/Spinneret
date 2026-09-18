--[[
sync_accounts.lua (owner: hotstate)

Materializes one account of a site (spec §5, "acc" and "accm"): writes the
account hash, rebuilds or extends the member set and pushes the ready-queue
scores of members to their availability with ZADD XX GT (spec §5.6).

A push costs one availability evaluation per member x endpoint group, so the
caller bounds pushes separately (operation "p") from membership writes, which
are cheap and keep the member set replacement atomic for up to 1000 members.

"sct" is the change time (ms) of the lifecycle fields st/bu: apply.lua writes
it with every Redis-first change and this script writes the PostgreSQL change
time (the latest lifecycle state event of the account) whenever it applies the
PostgreSQL lifecycle fields. In authoritative mode the PostgreSQL st/bu win
unless Redis holds a newer change (sct greater than the PostgreSQL change
time) that is still in force (a ban whose end has passed never outlives an
older PostgreSQL state), so an automatic ban not yet persisted by the
StateWriter is not reverted.

KEYS[1] = "P:T:meta"

ARGV[1]  mode: "a" authoritative (st/bu from PostgreSQL unless Redis holds a
         newer change, see above) or "m" merge (keep st/bu present in Redis)
ARGV[2]  operation:
           "r" write the account hash and replace the member set with the members
           "a" add the members to the member set (the account hash is not written)
           "p" push only: neither the account hash nor the member set is written
ARGV[3]  push "1"/"0": push the scores of the members of this call
ARGV[4]  endpoint group hkeys of the site (CSV)
ARGV[5]  account hkey
ARGV[6]  st (active|banned|disabled)
ARGV[7]  bu (ms, -1 permanent, 0 none)
ARGV[8]  cd (ms, 0 none); the stored value becomes max(existing, cd)
ARGV[9]  PostgreSQL change time of st/bu (ms)
ARGV[10] now (ms)
ARGV[11..] member identity hkeys

Returns {cd_moved (0/1), members_added, scores_pushed}.
]]

local base = sp_base()
local merge = ARGV[1] == 'm'
local op = ARGV[2]
local egs = sp_split(ARGV[4], ',')
local a = ARGV[5]
local ak = base .. 'acc:' .. a
local mk = base .. 'accm:' .. a
local first = 11
local last = #ARGV
local now = sp_num(ARGV[10], 0)

-- Reports whether the st/bu fields stored in Redis are kept.
local function keep_redis_lifecycle(st, cur_sct, pg_sct, bu)
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
  return true
end

local moved = false
local added = 0
if op == 'r' then
  local cur = redis.call('HMGET', ak, 'st', 'cd', 'bu', 'sct')
  local old_cd = sp_num(cur[2], 0)
  local cd = sp_num(ARGV[8], 0)
  if old_cd > cd then
    cd = old_cd
  end
  local pg_sct = sp_num(ARGV[9], 0)
  if keep_redis_lifecycle(cur[1], cur[4], pg_sct, cur[3]) then
    redis.call('HSETNX', ak, 'bu', ARGV[7])
    redis.call('HSETNX', ak, 'sct', sp_int_str(pg_sct))
    redis.call('HSET', ak, 'cd', sp_int_str(cd))
  else
    redis.call('HSET', ak, 'st', ARGV[6], 'bu', ARGV[7], 'cd', sp_int_str(cd), 'sct', sp_int_str(pg_sct))
  end
  moved = cd > old_cd
  redis.call('DEL', mk)
end
if op == 'r' or op == 'a' then
  local CHUNK = 500
  local from = first
  while from <= last do
    local to = from + CHUNK - 1
    if to > last then to = last end
    added = added + redis.call('SADD', mk, unpack(ARGV, from, to))
    from = to + 1
  end
end

local pushed = 0
if ARGV[3] == '1' then
  for idx = first, last do
    local i = ARGV[idx]
    for _, eg in ipairs(egs) do
      local s = sp_avail(base, eg, i)
      pushed = pushed + redis.call('ZADD', base .. 'rdy:' .. eg, 'XX', 'GT', 'CH', sp_score_str(s), i)
    end
  end
end

local moved_flag = 0
if moved then moved_flag = 1 end
return { moved_flag, added, pushed }
