--[[
sync_identities.lua (owner: hotstate)

Materializes identities of one site from PostgreSQL truth (spec §5.2, §5.6):
identity hash fields, account hashes, account membership, ready-queue
membership and scores, optional health/failure resets. Hot counters written by
other scripts (al xl scd sru gs gts gn gnf glf lu rbd rbn) are never
overwritten; the global score fields are only deleted when health is reset and
the global failure streak (gnf/glf) when health or failures are reset.

Lifecycle fields (identity st bu qu act, account st bu) carry their change time
"sct" (ms). apply.lua writes it with every Redis-first lifecycle change, and
this script writes the PostgreSQL state_changed_at whenever it applies the
PostgreSQL lifecycle fields. In authoritative mode the PostgreSQL lifecycle
fields are applied unless Redis holds a newer change (sct greater than the
PostgreSQL change time) that is still in force: a temporary ban or quarantine
whose end has passed never outlives an older PostgreSQL state. So an automatic
ban that the StateWriter has not persisted yet is never reverted by a stale
row, while PostgreSQL-first transitions (which set state_changed_at = now) win.

The proxy binding "px" is Redis-first in every mode: acquire.lua binds
identities in bind_identity mode and PostgreSQL proxy_bindings may lag behind
(or not be persisted at all), so a binding already stored in Redis wins as long
as its proxy is still materialized on the site; otherwise the PostgreSQL
binding (or "") is written.

KEYS[1] = "P:T:meta"

ARGV[1]  now (ms)
ARGV[2]  identity mode: "a" authoritative (PostgreSQL lifecycle fields win
         unless Redis holds a newer change, see above) or "m" merge
         (lifecycle fields already present in Redis win)
ARGV[3]  account mode: "a" or "m" (same meaning for the account st/bu fields;
         cd is always max(Redis, PostgreSQL))
ARGV[4]  reset health "1"/"0": the health entry of every endpoint group is
         reset to the group baseline (score = baseline, sts = now, samples,
         nfail and lastfail = 0) while cd, ru and lu are kept; gs/gts/gn and
         gnf/glf are deleted
ARGV[5]  reset failures "1"/"0" (nfail/lastfail and gnf/glf; ignored when
         health is reset, which implies it)
ARGV[6]  cold start "1"/"0": ready scores <= now become now + jitter
ARGV[7]  health baselines "eg:baseline,..." of the endpoint groups (read only
         when health is reset; missing groups use 70)
ARGV[8]  P, the number of group profiles, followed by P pairs:
           eligible endpoint group hkeys (CSV), other endpoint group hkeys (CSV)
then     A, the number of accounts, followed by A quintuples:
           account hkey, st, bu (-1 permanent), cd (ms), sct (ms)
then     identity records of 16 values each:
           op ("S" sync | "R" remove), identity hkey, iid, st, ty, tv, pv,
           acc (account hkey or ""), rg, bu, qu, act, px (proxy hkey or ""),
           profile (1-based), cold-start jitter (ms), sct (ms)

Returns {synced, removed, ready_members_written}.
]]

local base = sp_base()
local now = sp_num(ARGV[1], 0)
local merge = ARGV[2] == 'm'
local merge_accounts = ARGV[3] == 'm'
local reset_health = ARGV[4] == '1'
local reset_failures = (not reset_health) and ARGV[5] == '1'
local cold = ARGV[6] == '1'
local dirty = base .. 'dirty'
local DEFAULT_BASELINE = 70

local baselines = {}
if reset_health then
  for _, pair in ipairs(sp_split(ARGV[7], ',')) do
    local sep = string.find(pair, ':', 1, true)
    if sep then
      baselines[string.sub(pair, 1, sep - 1)] = sp_num(string.sub(pair, sep + 1), DEFAULT_BASELINE)
    end
  end
end

local function baseline_of(eg)
  return baselines[eg] or DEFAULT_BASELINE
end

-- Lifecycle fields of Redis are kept when they are present and either the mode
-- is merge or they carry a change newer than PostgreSQL's that is still in
-- force (bu/qu are the ban and quarantine ends, 0 when none, -1 permanent).
local function keep_redis_lifecycle(merge_mode, st, cur_sct, pg_sct, bu, qu)
  if not st then
    return false
  end
  if merge_mode then
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

local pos = 8
local nprof = sp_num(ARGV[pos], 0)
pos = pos + 1
local profiles = {}
for p = 1, nprof do
  profiles[p] = { sp_split(ARGV[pos], ','), sp_split(ARGV[pos + 1], ',') }
  pos = pos + 2
end

local nacc = sp_num(ARGV[pos], 0)
pos = pos + 1
for _ = 1, nacc do
  local ak = base .. 'acc:' .. ARGV[pos]
  local cur = redis.call('HMGET', ak, 'st', 'cd', 'bu', 'sct')
  local cd = sp_num(ARGV[pos + 3], 0)
  local existing_cd = sp_num(cur[2], 0)
  if existing_cd > cd then
    cd = existing_cd
  end
  local pg_sct = sp_num(ARGV[pos + 4], 0)
  if keep_redis_lifecycle(merge_accounts, cur[1], cur[4], pg_sct, cur[3], 0) then
    redis.call('HSETNX', ak, 'bu', ARGV[pos + 2])
    redis.call('HSETNX', ak, 'sct', sp_int_str(pg_sct))
    redis.call('HSET', ak, 'cd', sp_int_str(cd))
  else
    redis.call('HSET', ak, 'st', ARGV[pos + 1], 'bu', ARGV[pos + 2], 'cd', sp_int_str(cd), 'sct', sp_int_str(pg_sct))
  end
  pos = pos + 5
end

local function remove_identity(i, prof)
  local idk = base .. 'id:' .. i
  local prev_acc = redis.call('HGET', idk, 'acc')
  redis.call('DEL', idk, base .. 'bans:' .. i, base .. 'xa:i:' .. i)
  for _, list in ipairs(prof) do
    for _, eg in ipairs(list) do
      redis.call('ZREM', base .. 'rdy:' .. eg, i)
      redis.call('HDEL', base .. 'hs:' .. eg, i)
      redis.call('DEL', base .. 'q:' .. eg .. ':' .. i)
    end
  end
  if prev_acc and prev_acc ~= '' then
    redis.call('SREM', base .. 'accm:' .. prev_acc, i)
  end
end

local synced, removed, written = 0, 0, 0
local REC = 16
local nargs = #ARGV

while pos + REC - 1 <= nargs do
  local op = ARGV[pos]
  local i = ARGV[pos + 1]
  local prof = profiles[sp_num(ARGV[pos + 13], 0)] or { {}, {} }

  if op == 'R' then
    remove_identity(i, prof)
    removed = removed + 1
  else
    local idk = base .. 'id:' .. i
    local cur = redis.call('HMGET', idk, 'acc', 'st', 'bu', 'qu', 'act', 'px', 'scd', 'sru', 'al', 'xl', 'sct')
    local st, bu, qu, act, px = ARGV[pos + 3], ARGV[pos + 9], ARGV[pos + 10], ARGV[pos + 11], ARGV[pos + 12]
    local sct = sp_num(ARGV[pos + 15], 0)
    if keep_redis_lifecycle(merge, cur[2], cur[11], sct, cur[3], cur[4]) then
      st = cur[2]
      if cur[3] then bu = cur[3] end
      if cur[4] then qu = cur[4] end
      if cur[5] then act = cur[5] end
      if cur[11] then sct = sp_num(cur[11], 0) end
    end
    local cur_px = cur[6]
    if cur_px and cur_px ~= '' and cur_px ~= px and redis.call('EXISTS', base .. 'px:' .. cur_px) == 1 then
      px = cur_px
    end
    local acc = ARGV[pos + 7]
    redis.call('HSET', idk,
      'iid', ARGV[pos + 2], 'st', st, 'ty', ARGV[pos + 4], 'tv', ARGV[pos + 5], 'pv', ARGV[pos + 6],
      'acc', acc, 'rg', ARGV[pos + 8], 'bu', bu, 'qu', qu, 'act', act, 'px', px, 'sct', sp_int_str(sct))

    local prev_acc = cur[1]
    if prev_acc and prev_acc ~= '' and prev_acc ~= acc then
      redis.call('SREM', base .. 'accm:' .. prev_acc, i)
    end
    if acc ~= '' then
      redis.call('SADD', base .. 'accm:' .. acc, i)
    end
    if reset_health or reset_failures then
      -- The worker-owned global failure streak (not snapshotted).
      redis.call('HDEL', idk, 'gnf', 'glf')
    end
    if reset_health then
      redis.call('HDEL', idk, 'gs', 'gts', 'gn')
      redis.call('SADD', dirty, 'g' .. i)
    end

    -- Identity-level part of sp_avail (spec §5.6), computed once per identity.
    local m = sp_num(cur[7], 0)
    local sru = sp_num(cur[8], 0)
    if sru > m then m = sru end
    if sp_num(cur[9], 0) > 0 then
      local xl = sp_num(cur[10], 0)
      if xl > m then m = xl end
    end
    if acc ~= '' then
      local acd = sp_num(redis.call('HGET', base .. 'acc:' .. acc, 'cd'), 0)
      if acd > m then m = acd end
    end

    local ready = st == 'active' or st == 'pending'
    local jitter = sp_num(ARGV[pos + 14], 0)
    local resetting = reset_health or reset_failures
    for li, list in ipairs(prof) do
      local eligible = ready and li == 1
      for _, eg in ipairs(list) do
        local hk = base .. 'hs:' .. eg
        local rk = base .. 'rdy:' .. eg
        local packed = false
        if resetting then
          packed = redis.call('HGET', hk, i)
          if packed and reset_health then
            -- Scheduling state (cd, ru, lu) is kept, like apply.lua resets.
            local t = sp_hs_unpack(packed, 0, 0)
            t.score = baseline_of(eg)
            t.sts = now
            t.samples = 0
            t.nfail = 0
            t.lastfail = 0
            packed = sp_hs_set(base, eg, i, t)
            redis.call('SADD', dirty, 'e' .. eg .. ':' .. i)
          elseif packed then
            local t = sp_hs_unpack(packed, 0, 0)
            if t.nfail ~= 0 or t.lastfail ~= 0 then
              t.nfail = 0
              t.lastfail = 0
              packed = sp_hs_set(base, eg, i, t)
              redis.call('SADD', dirty, 'e' .. eg .. ':' .. i)
            end
          end
        end

        if not eligible then
          redis.call('ZREM', rk, i)
        elseif not (merge and redis.call('ZSCORE', rk, i)) then
          -- Merge mode keeps the score of existing members: Redis-first
          -- writers (acquire, release, apply) maintain it.
          if not resetting then
            packed = redis.call('HGET', hk, i)
          end
          local s = m
          if packed then
            local t = sp_hs_unpack(packed, 0, 0)
            if t.cd > s then s = t.cd end
            if t.ru > s then s = t.ru end
          end
          if cold and s <= now then
            s = now + jitter
          end
          redis.call('ZADD', rk, sp_score_str(s), i)
          written = written + 1
        end
      end
    end
    synced = synced + 1
  end
  pos = pos + REC
end

return { synced, removed, written }
