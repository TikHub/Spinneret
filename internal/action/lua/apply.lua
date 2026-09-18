--[[
apply.lua (owner: internal/action) — applies dispositions and lifecycle
changes to the hot state of ONE site atomically (spec §5, §6.6).

KEYS[1] = "P:T:meta" of the site.
ARGV[1] = now (Unix ms)
ARGV[2] = idempotency token ("" = none). With a token the results of the first
          call are recorded in "P:T:axr:<token>" for 15 minutes and returned
          unchanged by any later call with the same token, without applying
          the operations again. The executor passes the report (lease and
          report id), so a worker retry after a client-side timeout gets the
          original results back and the state changes are still persisted.
ARGV[3..] = one JSON object per operation:
  op   "cd" cooldown | "ban" | "exp" expire | "qua" quarantine | "act" activate |
       "set" manual state set | "clr" clear cooldown (revert) | "rst" reset stats
  sc   scope: "ie" identity x endpoint group, "is" identity x site, "ac" account,
       "ps" proxy x site, "pg" proxy global (on this site), "id" identity, "px" proxy
  s    subject hkey (identity, account or proxy)
  eg   endpoint group hkey (scope "ie")
  u    until ms (-1 permanent, 0 none)
  f    flags: 1 automatic (record rcd/rcds), 2 force (skip severity rules),
       4 reset failures, 8 reset health, 16 record ban history
       Identity-level resets ("set", "clr" scope "is") with 4 also clear the
       worker-owned global failure streak gnf/glf of id:<i>; with 8 they clear
       the global score gs/gts/gn. "clr" scope "ie" only resets the endpoint
       group entry.
  egs  endpoint group hkeys touched by identity-level pushes and removals
  add  endpoint group hkeys an identity is (re-)added to when it becomes
       schedulable ("set" to pending/active)
  to   target state of "set"
  trim rcd retention ms (automatic identity x endpoint cooldowns)
  pn   1 when the report being processed already incremented the streak
       (the rcd record stores the streak before the report)
  bl   default health baseline, bls {eg: baseline} per endpoint group
  cnt  counter key suffixes ("cnt:i<hkey>:<outcome>:<windowMs>") deleted by "rst"
  gl   1 when "rst" also resets the identity global score (gs/gts/gn), the
       global failure streak (gnf/glf) and counters

Every lifecycle change (st/bu/qu/act of "id", st/bu of "acc" and "px") also
writes "sct" = now, the change time the hot-state synchronization compares
with the PostgreSQL state change time before it applies PostgreSQL lifecycle
fields. "set" is a PostgreSQL-first push whose now is the time of the
committed change: it is skipped (reason newer_state) when the subject already
holds a later Redis-first change (sct > now), which the StateWriter persists.

Returns one entry per operation:
  {applied 0|1, reason, from state, to state, effective until (string), members}
where members lists "hkey|iid|from|to" for account operations that changed
member identities. Skip reasons: missing, already_cooling, already_banned,
more_severe, already_expired, already_quarantined, not_applicable,
not_pending, not_cooling, newer_state, invalid.

Hot-path conventions (spec §5.7): the operations are decoded before anything
else so that only the handlers this call needs are created. A chunk-level
closure with upvalues costs about 0.38 us per script call on Valkey 8.1, and
the report path — one automatic identity x endpoint cooldown — must not pay for
eight handlers, five helpers it cannot reach and two dispatch tables (measured
7.9 us of 42, 19 % of the script). Integral redis.call arguments travel as Lua
numbers, and the packed hs entry of a cooldown is spliced with sp_hs_raw
instead of being decoded and re-encoded field by field.
]]

local base = sp_base()
local now = sp_num(ARGV[1], 0)
local token = ARGV[2] or ''

local replay_key = nil
if token ~= '' then
  replay_key = base .. 'axr:' .. token
  local recorded = redis.call('GET', replay_key)
  if recorded then
    return cjson.decode(recorded)
  end
end

local REPLAY_TTL_MS = 900000        -- 15 minutes
local BAN_HISTORY_MS = 2592000000   -- 30 days
local SITE_CD_TRIM_MS = 600000      -- rcds retention (10 minutes)
local DEFAULT_BASELINE = 70

local F_AUTO, F_FORCE, F_RESET_FAIL, F_RESET_HEALTH, F_BANS = 1, 2, 4, 8, 16

-- Decode the operations and record which kinds occur, so that the definitions
-- below can be skipped for the kinds this call does not carry.
local ops = {}
local kinds = {}
for idx = 3, #ARGV do
  local op = cjson.decode(ARGV[idx])
  op.egs = (type(op.egs) == 'table') and op.egs or {}
  op.add = (type(op.add) == 'table') and op.add or {}
  op.cnt = (type(op.cnt) == 'table') and op.cnt or {}
  ops[idx - 2] = op
  if type(op.op) == 'string' then
    kinds[op.op] = true
  end
end
-- Every kind but a cooldown needs the five helpers below; a cooldown reaches
-- none of them.
local ap_other = kinds.ban or kinds.exp or kinds.qua or kinds.act or
  kinds.set or kinds.clr or kinds.rst

local function ap_has(flags, bit)
  return math.floor(sp_num(flags, 0) / bit) % 2 == 1
end

local function ap_result(applied, reason, from, to, u, members)
  return { applied and 1 or 0, reason or '', from or '', to or '', sp_int_str(u), members or {} }
end

-- ap_id_get reads the lifecycle fields and the identity side of the
-- availability rule (spec §5.6) in one HMGET, so that a handler that changes a
-- cooldown can recompute the ready score without reading the hash again.
local function ap_id_get(i)
  local v = redis.call('HMGET', base .. 'id:' .. i,
    'st', 'bu', 'qu', 'scd', 'iid', 'sct', 'sru', 'al', 'xl', 'acc')
  if not v[1] then
    return nil
  end
  return { st = v[1], bu = sp_num(v[2], 0), qu = sp_num(v[3], 0), scd = sp_num(v[4], 0), iid = v[5] or '',
    sct = sp_num(v[6], 0), sru = sp_num(v[7], 0), al = sp_num(v[8], 0), xl = sp_num(v[9], 0),
    acc = v[10] or '' }
end

-- ap_avail is sp_avail for a caller that already holds the identity hash and
-- the two hs fields: only an account cooldown still has to be read.
local function ap_avail(id, cd, ru)
  local m = cd
  if ru > m then m = ru end
  if id.scd > m then m = id.scd end
  if id.sru > m then m = id.sru end
  if id.al > 0 and id.xl > m then m = id.xl end
  if id.acc ~= '' then
    local acd = sp_num(redis.call('HGET', base .. 'acc:' .. id.acc, 'cd'), 0)
    if acd > m then m = acd end
  end
  return m
end

local function ap_baseline(op, eg)
  if type(op.bls) == 'table' then
    local b = op.bls[sp_str(eg)]
    if type(b) == 'number' then
      return b
    end
  end
  if type(op.bl) == 'number' then
    return op.bl
  end
  return DEFAULT_BASELINE
end

local ap_zrem_all, ap_reset_hs, ap_reset_identity, ap_record_ban, ap_ban_identity
if ap_other then
  ap_zrem_all = function(egs, i)
    for _, eg in ipairs(egs) do
      redis.call('ZREM', base .. 'rdy:' .. sp_str(eg), i)
    end
  end

  -- Resets the packed health entry of identity i on eg (only when present).
  ap_reset_hs = function(op, eg, i, fails, health)
    local key = base .. 'hs:' .. sp_str(eg)
    local raw = redis.call('HGET', key, i)
    if not raw then
      return
    end
    local t = sp_hs_unpack(raw, ap_baseline(op, eg), now)
    if fails then
      t.nfail = 0
      t.lastfail = 0
    end
    if health then
      t.score = ap_baseline(op, eg)
      t.sts = now
      t.samples = 0
    end
    redis.call('HSET', key, i, sp_hs_pack(t))
    redis.call('SADD', base .. 'dirty', 'e' .. sp_str(eg) .. ':' .. i)
  end

  -- Resets the identity x endpoint entries of i on egs and its global fields:
  -- fails clears the global failure streak (gnf/glf, owned by observe.lua and
  -- not snapshotted), health the global score (gs/gts/gn, snapshotted).
  ap_reset_identity = function(op, egs, i, fails, health)
    if not fails and not health then
      return
    end
    for _, eg in ipairs(egs) do
      ap_reset_hs(op, eg, i, fails, health)
    end
    if fails then
      redis.call('HDEL', base .. 'id:' .. i, 'gnf', 'glf')
    end
    if health then
      redis.call('HDEL', base .. 'id:' .. i, 'gs', 'gts', 'gn')
      redis.call('SADD', base .. 'dirty', 'g' .. i)
    end
  end

  ap_record_ban = function(i)
    local key = base .. 'bans:' .. i
    redis.call('ZADD', key, now, sp_int_str(now))
    redis.call('ZREMRANGEBYSCORE', key, '-inf', '(' .. (now - BAN_HISTORY_MS))
    redis.call('PEXPIRE', key, BAN_HISTORY_MS)
  end

  -- Bans identity i; shared by identity and account bans.
  ap_ban_identity = function(op, i, u)
    local id = ap_id_get(i)
    if not id then
      return false, 'missing', ''
    end
    if not ap_has(op.f, F_FORCE) then
      if id.st == 'retired' or id.st == 'disabled' then
        return false, 'not_applicable', id.st
      end
      if id.st == 'banned' and (id.bu == -1 or (u ~= -1 and id.bu >= u)) then
        return false, 'already_banned', id.st
      end
    end
    redis.call('HSET', base .. 'id:' .. i, 'st', 'banned', 'bu', u, 'qu', 0, 'sct', now)
    ap_zrem_all(op.egs, i)
    if ap_has(op.f, F_BANS) then
      ap_record_ban(i)
    end
    return true, '', id.st
  end
end

local ap_cooldown
if kinds.cd then
  ap_cooldown = function(op)
    local s = sp_str(op.s)
    local u = sp_num(op.u, 0)
    local force = ap_has(op.f, F_FORCE)
    if u <= 0 then
      return ap_result(false, 'invalid', '', '', 0)
    end
    if op.sc == 'ie' or op.sc == 'is' then
      local id = ap_id_get(s)
      if not id then
        return ap_result(false, 'missing', '', '', 0)
      end
      if op.sc == 'ie' then
        local eg = sp_str(op.eg)
        local hs_key = base .. 'hs:' .. eg
        -- Only "cd" changes; the other seven fields travel back as the strings
        -- they came in as.
        local raw = redis.call('HGET', hs_key, s)
        local f1, f2, f3, f4, f5, cd_s, ru_s, lu_s = sp_hs_raw(raw)
        if f1 == nil then
          -- Missing entry, or a short form written by an older release.
          f1, f2, f3, f4, f5, cd_s, ru_s, lu_s =
            sp_hs_raw(sp_hs_pack(sp_hs_unpack(raw, ap_baseline(op, eg), now)))
        end
        local prev_cd = sp_num(cd_s, 0)
        if prev_cd >= u and not force then
          return ap_result(false, 'already_cooling', id.st, id.st, prev_cd)
        end
        local prev_nfail = sp_num(f4, 0) - sp_num(op.pn, 0)
        if prev_nfail < 0 then
          prev_nfail = 0
        end
        redis.call('HSET', hs_key, s,
          f1 .. '|' .. f2 .. '|' .. f3 .. '|' .. f4 .. '|' .. f5 .. '|' .. u .. '|' .. ru_s .. '|' .. lu_s)
        if ap_has(op.f, F_AUTO) then
          local key = base .. 'rcd:' .. eg
          local trim = sp_num(op.trim, SITE_CD_TRIM_MS)
          redis.call('ZADD', key, now, s .. '|' .. sp_int_str(prev_cd) .. '|' .. sp_int_str(prev_nfail))
          redis.call('ZREMRANGEBYSCORE', key, '-inf', '(' .. (now - trim))
          redis.call('PEXPIRE', key, trim)
        end
        -- The new availability follows from the state already in hand
        -- (spec §5.6): no second read of the hs entry and the identity hash.
        redis.call('ZADD', base .. 'rdy:' .. eg, 'XX', ap_avail(id, u, sp_num(ru_s, 0)), s)
        redis.call('SADD', base .. 'dirty', 'e' .. eg .. ':' .. s)
        return ap_result(true, '', id.st, id.st, u)
      end
      if id.scd >= u and not force then
        return ap_result(false, 'already_cooling', id.st, id.st, id.scd)
      end
      redis.call('HSET', base .. 'id:' .. s, 'scd', u)
      if ap_has(op.f, F_AUTO) then
        local key = base .. 'rcds'
        redis.call('ZADD', key, now, s .. '|' .. sp_int_str(id.scd))
        redis.call('ZREMRANGEBYSCORE', key, '-inf', '(' .. (now - SITE_CD_TRIM_MS))
        redis.call('PEXPIRE', key, SITE_CD_TRIM_MS)
      end
      sp_push_all(base, op.egs, s, u)
      redis.call('SADD', base .. 'dirty', 'g' .. s)
      return ap_result(true, '', id.st, id.st, u)
    elseif op.sc == 'ac' then
      local akey = base .. 'acc:' .. s
      local a = redis.call('HMGET', akey, 'st', 'cd')
      if not a[1] then
        return ap_result(false, 'missing', '', '', 0)
      end
      local prev = sp_num(a[2], 0)
      if prev >= u and not force then
        return ap_result(false, 'already_cooling', a[1], a[1], prev)
      end
      redis.call('HSET', akey, 'cd', u)
      for _, m in ipairs(redis.call('SMEMBERS', base .. 'accm:' .. s)) do
        sp_push_all(base, op.egs, m, u)
      end
      return ap_result(true, '', a[1], a[1], u)
    elseif op.sc == 'ps' or op.sc == 'pg' then
      local pkey = base .. 'px:' .. s
      local field = (op.sc == 'ps') and 'cd' or 'gcd'
      local p = redis.call('HMGET', pkey, 'st', field)
      if not p[1] then
        return ap_result(false, 'missing', '', '', 0)
      end
      local prev = sp_num(p[2], 0)
      if prev >= u and not force then
        return ap_result(false, 'already_cooling', p[1], p[1], prev)
      end
      redis.call('HSET', pkey, field, u)
      redis.call('ZADD', base .. 'pxrdy', 'XX', 'GT', u, s)
      redis.call('SADD', base .. 'dirty', 'p' .. s)
      return ap_result(true, '', p[1], p[1], u)
    end
    return ap_result(false, 'invalid', '', '', 0)
  end
end

local ap_ban
if kinds.ban then
  ap_ban = function(op)
    local s = sp_str(op.s)
    local u = sp_num(op.u, 0)
    if u == 0 then
      return ap_result(false, 'invalid', '', '', 0)
    end
    local force = ap_has(op.f, F_FORCE)
    if op.sc == 'id' then
      local ok, reason, from = ap_ban_identity(op, s, u)
      if not ok then
        return ap_result(false, reason, from, from, 0)
      end
      return ap_result(true, '', from, 'banned', u)
    elseif op.sc == 'ac' then
      local akey = base .. 'acc:' .. s
      local a = redis.call('HMGET', akey, 'st', 'bu')
      if not a[1] then
        return ap_result(false, 'missing', '', '', 0)
      end
      local bu = sp_num(a[2], 0)
      if not force then
        if a[1] == 'disabled' then
          return ap_result(false, 'not_applicable', a[1], a[1], 0)
        end
        if a[1] == 'banned' and (bu == -1 or (u ~= -1 and bu >= u)) then
          return ap_result(false, 'already_banned', a[1], a[1], bu)
        end
      end
      redis.call('HSET', akey, 'st', 'banned', 'bu', u, 'sct', now)
      local members = {}
      for _, m in ipairs(redis.call('SMEMBERS', base .. 'accm:' .. s)) do
        local iid = redis.call('HGET', base .. 'id:' .. m, 'iid') or ''
        local ok, _, from = ap_ban_identity(op, m, u)
        if ok then
          members[#members + 1] = m .. '|' .. iid .. '|' .. from .. '|banned'
        end
      end
      return ap_result(true, '', a[1], 'banned', u, members)
    elseif op.sc == 'px' then
      local pkey = base .. 'px:' .. s
      local p = redis.call('HMGET', pkey, 'st', 'bu')
      if not p[1] then
        return ap_result(false, 'missing', '', '', 0)
      end
      local bu = sp_num(p[2], 0)
      if not force then
        if p[1] == 'retired' or p[1] == 'disabled' then
          return ap_result(false, 'not_applicable', p[1], p[1], 0)
        end
        if p[1] == 'banned' and (bu == -1 or (u ~= -1 and (bu == 0 or bu >= u))) then
          return ap_result(false, 'already_banned', p[1], p[1], bu)
        end
      end
      redis.call('HSET', pkey, 'st', 'banned', 'bu', u, 'sct', now)
      redis.call('ZREM', base .. 'pxrdy', s)
      return ap_result(true, '', p[1], 'banned', u)
    end
    return ap_result(false, 'invalid', '', '', 0)
  end
end

local ap_expire
if kinds.exp then
  ap_expire = function(op)
    local s = sp_str(op.s)
    local id = ap_id_get(s)
    if not id then
      return ap_result(false, 'missing', '', '', 0)
    end
    if not ap_has(op.f, F_FORCE) then
      if id.st == 'banned' then
        return ap_result(false, 'more_severe', id.st, id.st, 0)
      elseif id.st == 'expired' then
        return ap_result(false, 'already_expired', id.st, id.st, 0)
      elseif id.st == 'retired' or id.st == 'disabled' then
        return ap_result(false, 'not_applicable', id.st, id.st, 0)
      end
    end
    redis.call('HSET', base .. 'id:' .. s, 'st', 'expired', 'bu', 0, 'qu', 0, 'sct', now)
    ap_zrem_all(op.egs, s)
    return ap_result(true, '', id.st, 'expired', 0)
  end
end

local ap_quarantine
if kinds.qua then
  ap_quarantine = function(op)
    local s = sp_str(op.s)
    local u = sp_num(op.u, 0)
    if u <= 0 then
      return ap_result(false, 'invalid', '', '', 0)
    end
    local force = ap_has(op.f, F_FORCE)
    if op.sc == 'id' then
      local id = ap_id_get(s)
      if not id then
        return ap_result(false, 'missing', '', '', 0)
      end
      if not force then
        if id.st == 'banned' or id.st == 'expired' then
          return ap_result(false, 'more_severe', id.st, id.st, 0)
        elseif id.st == 'retired' or id.st == 'disabled' then
          return ap_result(false, 'not_applicable', id.st, id.st, 0)
        elseif id.st == 'quarantined' and id.qu >= u then
          return ap_result(false, 'already_quarantined', id.st, id.st, id.qu)
        end
      end
      redis.call('HSET', base .. 'id:' .. s, 'st', 'quarantined', 'qu', u, 'bu', 0, 'sct', now)
      ap_zrem_all(op.egs, s)
      return ap_result(true, '', id.st, 'quarantined', u)
    elseif op.sc == 'px' then
      local pkey = base .. 'px:' .. s
      local p = redis.call('HMGET', pkey, 'st', 'qu')
      if not p[1] then
        return ap_result(false, 'missing', '', '', 0)
      end
      if not force then
        if p[1] == 'banned' then
          return ap_result(false, 'more_severe', p[1], p[1], 0)
        elseif p[1] == 'retired' or p[1] == 'disabled' then
          return ap_result(false, 'not_applicable', p[1], p[1], 0)
        elseif p[1] == 'quarantined' and sp_num(p[2], 0) >= u then
          return ap_result(false, 'already_quarantined', p[1], p[1], sp_num(p[2], 0))
        end
      end
      redis.call('HSET', pkey, 'st', 'quarantined', 'qu', u, 'sct', now)
      redis.call('ZREM', base .. 'pxrdy', s)
      return ap_result(true, '', p[1], 'quarantined', u)
    end
    return ap_result(false, 'invalid', '', '', 0)
  end
end

local ap_activate
if kinds.act then
  ap_activate = function(op)
    local s = sp_str(op.s)
    local id = ap_id_get(s)
    if not id then
      return ap_result(false, 'missing', '', '', 0)
    end
    if id.st ~= 'pending' then
      return ap_result(false, 'not_pending', id.st, id.st, 0)
    end
    redis.call('HSET', base .. 'id:' .. s, 'st', 'active', 'act', now, 'sct', now)
    for _, eg in ipairs(op.egs) do
      sp_rescore(base, eg, s)
    end
    return ap_result(true, '', 'pending', 'active', 0)
  end
end

-- The states "set" accepts, as one constant string instead of a table: a
-- seven-entry table constructor costs half a microsecond on every call.
local STATE_LIST = ',pending,active,expired,banned,quarantined,disabled,retired,'

local ap_set
if kinds.set then
  ap_set = function(op)
    local s = sp_str(op.s)
    local to = op.to
    if op.sc == 'ac' then
      local akey = base .. 'acc:' .. s
      local a = redis.call('HMGET', akey, 'st', 'sct')
      local st = a[1]
      if not st then
        return ap_result(false, 'missing', '', '', 0)
      end
      if to ~= 'active' and to ~= 'banned' and to ~= 'disabled' then
        return ap_result(false, 'invalid', st, st, 0)
      end
      if sp_num(a[2], 0) > now then
        return ap_result(false, 'newer_state', st, st, 0)
      end
      local u = (to == 'banned') and sp_num(op.u, 0) or 0
      redis.call('HSET', akey, 'st', to, 'bu', u, 'sct', now)
      return ap_result(true, '', st, to, u)
    end
    if type(to) ~= 'string' or to == '' or not string.find(STATE_LIST, ',' .. to .. ',', 1, true) then
      return ap_result(false, 'invalid', '', '', 0)
    end
    local id = ap_id_get(s)
    if not id then
      return ap_result(false, 'missing', '', '', 0)
    end
    if id.sct > now then
      return ap_result(false, 'newer_state', id.st, id.st, 0)
    end
    local bu, qu = 0, 0
    if to == 'banned' then
      bu = sp_num(op.u, 0)
    elseif to == 'quarantined' then
      qu = sp_num(op.u, 0)
    end
    local key = base .. 'id:' .. s
    redis.call('HSET', key, 'st', to, 'bu', bu, 'qu', qu, 'sct', now)
    if to == 'active' and id.st ~= 'active' then
      redis.call('HSET', key, 'act', now)
    end
    ap_reset_identity(op, op.egs, s, ap_has(op.f, F_RESET_FAIL), ap_has(op.f, F_RESET_HEALTH))
    if to == 'active' or to == 'pending' then
      local eligible = {}
      for _, eg in ipairs(op.add) do
        eligible[sp_str(eg)] = true
        redis.call('ZADD', base .. 'rdy:' .. sp_str(eg), sp_score_str(sp_avail(base, eg, s)), s)
      end
      for _, eg in ipairs(op.egs) do
        if not eligible[sp_str(eg)] then
          redis.call('ZREM', base .. 'rdy:' .. sp_str(eg), s)
        end
      end
    else
      ap_zrem_all(op.egs, s)
    end
    if to == 'banned' and ap_has(op.f, F_BANS) then
      ap_record_ban(s)
    end
    local u = bu
    if to == 'quarantined' then
      u = qu
    end
    return ap_result(true, '', id.st, to, u)
  end
end

local ap_clear
if kinds.clr then
  ap_clear = function(op)
    local s = sp_str(op.s)
    local limit = sp_num(op.u, 0)
    local id = ap_id_get(s)
    if not id then
      return ap_result(false, 'missing', '', '', 0)
    end
    local fails, health = ap_has(op.f, F_RESET_FAIL), ap_has(op.f, F_RESET_HEALTH)
    if op.sc == 'ie' then
      local eg = sp_str(op.eg)
      local t = sp_hs_get(base, eg, s, ap_baseline(op, eg), now)
      if t.cd <= now or (limit > 0 and t.cd > limit) then
        return ap_result(false, 'not_cooling', id.st, id.st, t.cd)
      end
      local prev = t.cd
      t.cd = 0
      if fails then
        t.nfail = 0
        t.lastfail = 0
      end
      if health then
        t.score = ap_baseline(op, eg)
        t.sts = now
        t.samples = 0
      end
      sp_hs_set(base, eg, s, t)
      redis.call('ZADD', base .. 'rdy:' .. eg, 'XX', ap_avail(id, 0, t.ru), s)
      redis.call('SADD', base .. 'dirty', 'e' .. eg .. ':' .. s)
      return ap_result(true, '', id.st, id.st, prev)
    elseif op.sc == 'is' then
      if id.scd <= now or (limit > 0 and id.scd > limit) then
        return ap_result(false, 'not_cooling', id.st, id.st, id.scd)
      end
      redis.call('HSET', base .. 'id:' .. s, 'scd', 0)
      ap_reset_identity(op, op.egs, s, fails, health)
      for _, eg in ipairs(op.egs) do
        sp_rescore(base, eg, s)
      end
      redis.call('SADD', base .. 'dirty', 'g' .. s)
      return ap_result(true, '', id.st, id.st, id.scd)
    end
    return ap_result(false, 'invalid', id.st, id.st, 0)
  end
end

local ap_reset_stats
if kinds.rst then
  ap_reset_stats = function(op)
    local s = sp_str(op.s)
    local id = ap_id_get(s)
    if not id then
      return ap_result(false, 'missing', '', '', 0)
    end
    for _, eg in ipairs(op.egs) do
      ap_reset_hs(op, eg, s, true, true)
    end
    if sp_num(op.gl, 0) == 1 then
      redis.call('HDEL', base .. 'id:' .. s, 'gs', 'gts', 'gn', 'gnf', 'glf')
      redis.call('SADD', base .. 'dirty', 'g' .. s)
      for _, suffix in ipairs(op.cnt) do
        redis.call('DEL', base .. suffix)
      end
    end
    return ap_result(true, '', id.st, id.st, 0)
  end
end

local out = {}
for i = 1, #ops do
  local op = ops[i]
  local k = op.op
  if k == 'cd' then
    out[i] = ap_cooldown(op)
  elseif k == 'ban' then
    out[i] = ap_ban(op)
  elseif k == 'exp' then
    out[i] = ap_expire(op)
  elseif k == 'qua' then
    out[i] = ap_quarantine(op)
  elseif k == 'act' then
    out[i] = ap_activate(op)
  elseif k == 'set' then
    out[i] = ap_set(op)
  elseif k == 'clr' then
    out[i] = ap_clear(op)
  elseif k == 'rst' then
    out[i] = ap_reset_stats(op)
  else
    out[i] = ap_result(false, 'invalid', '', '', 0)
  end
end
if replay_key then
  redis.call('SET', replay_key, cjson.encode(out), 'PX', REPLAY_TTL_MS)
end
return out
