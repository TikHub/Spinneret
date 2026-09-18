--[[
apply.lua (owner: internal/action) — frozen pre-optimization source, kept for
BenchmarkABWorker. The shipped script lives in internal/action/lua/apply.lua
and its header documents the contract; this copy exists only so the before and
after numbers of the worker-path optimization stay reproducible.
]]

local base = sp_base()
local now = sp_num(ARGV[1], 0)
local token = ARGV[2] or ''
local REPLAY_TTL_MS = 900000        -- 15 minutes

local replay_key = nil
if token ~= '' then
  replay_key = base .. 'axr:' .. token
  local recorded = redis.call('GET', replay_key)
  if recorded then
    return cjson.decode(recorded)
  end
end

local BAN_HISTORY_MS = 2592000000   -- 30 days
local SITE_CD_TRIM_MS = 600000      -- rcds retention (10 minutes)
local DEFAULT_BASELINE = 70

local F_AUTO, F_FORCE, F_RESET_FAIL, F_RESET_HEALTH, F_BANS = 1, 2, 4, 8, 16

local function ap_has(flags, bit)
  return math.floor(sp_num(flags, 0) / bit) % 2 == 1
end

local function ap_result(applied, reason, from, to, u, members)
  return { applied and 1 or 0, reason or '', from or '', to or '', sp_int_str(u), members or {} }
end

local function ap_id_get(i)
  local v = redis.call('HMGET', base .. 'id:' .. i, 'st', 'bu', 'qu', 'scd', 'iid', 'sct')
  if not v[1] then
    return nil
  end
  return { st = v[1], bu = sp_num(v[2], 0), qu = sp_num(v[3], 0), scd = sp_num(v[4], 0), iid = v[5] or '',
    sct = sp_num(v[6], 0) }
end

local function ap_zrem_all(egs, i)
  for _, eg in ipairs(egs) do
    redis.call('ZREM', base .. 'rdy:' .. sp_str(eg), i)
  end
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

-- Resets the packed health entry of identity i on eg (only when present).
local function ap_reset_hs(op, eg, i, fails, health)
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

local function ap_reset_identity(op, egs, i, fails, health)
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

local function ap_record_ban(i)
  local key = base .. 'bans:' .. i
  redis.call('ZADD', key, sp_score_str(now), sp_int_str(now))
  redis.call('ZREMRANGEBYSCORE', key, '-inf', '(' .. sp_score_str(now - BAN_HISTORY_MS))
  redis.call('PEXPIRE', key, BAN_HISTORY_MS)
end

-- Bans identity i; shared by identity and account bans.
local function ap_ban_identity(op, i, u)
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
  redis.call('HSET', base .. 'id:' .. i, 'st', 'banned', 'bu', sp_int_str(u), 'qu', '0', 'sct', sp_int_str(now))
  ap_zrem_all(op.egs, i)
  if ap_has(op.f, F_BANS) then
    ap_record_ban(i)
  end
  return true, '', id.st
end

local function ap_cooldown(op)
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
      local t = sp_hs_get(base, eg, s, ap_baseline(op, eg), now)
      if t.cd >= u and not force then
        return ap_result(false, 'already_cooling', id.st, id.st, t.cd)
      end
      local prev_cd, prev_nfail = t.cd, t.nfail - sp_num(op.pn, 0)
      if prev_nfail < 0 then
        prev_nfail = 0
      end
      t.cd = u
      sp_hs_set(base, eg, s, t)
      if ap_has(op.f, F_AUTO) then
        local key = base .. 'rcd:' .. eg
        local trim = sp_num(op.trim, SITE_CD_TRIM_MS)
        redis.call('ZADD', key, sp_score_str(now), s .. '|' .. sp_int_str(prev_cd) .. '|' .. sp_int_str(prev_nfail))
        redis.call('ZREMRANGEBYSCORE', key, '-inf', '(' .. sp_score_str(now - trim))
        redis.call('PEXPIRE', key, trim)
      end
      sp_rescore(base, eg, s)
      redis.call('SADD', base .. 'dirty', 'e' .. eg .. ':' .. s)
      return ap_result(true, '', id.st, id.st, u)
    end
    if id.scd >= u and not force then
      return ap_result(false, 'already_cooling', id.st, id.st, id.scd)
    end
    redis.call('HSET', base .. 'id:' .. s, 'scd', sp_int_str(u))
    if ap_has(op.f, F_AUTO) then
      local key = base .. 'rcds'
      redis.call('ZADD', key, sp_score_str(now), s .. '|' .. sp_int_str(id.scd))
      redis.call('ZREMRANGEBYSCORE', key, '-inf', '(' .. sp_score_str(now - SITE_CD_TRIM_MS))
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
    redis.call('HSET', akey, 'cd', sp_int_str(u))
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
    redis.call('HSET', pkey, field, sp_int_str(u))
    redis.call('ZADD', base .. 'pxrdy', 'XX', 'GT', sp_score_str(u), s)
    redis.call('SADD', base .. 'dirty', 'p' .. s)
    return ap_result(true, '', p[1], p[1], u)
  end
  return ap_result(false, 'invalid', '', '', 0)
end

local function ap_ban(op)
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
    redis.call('HSET', akey, 'st', 'banned', 'bu', sp_int_str(u), 'sct', sp_int_str(now))
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
    redis.call('HSET', pkey, 'st', 'banned', 'bu', sp_int_str(u), 'sct', sp_int_str(now))
    redis.call('ZREM', base .. 'pxrdy', s)
    return ap_result(true, '', p[1], 'banned', u)
  end
  return ap_result(false, 'invalid', '', '', 0)
end

local function ap_expire(op)
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
  redis.call('HSET', base .. 'id:' .. s, 'st', 'expired', 'bu', '0', 'qu', '0', 'sct', sp_int_str(now))
  ap_zrem_all(op.egs, s)
  return ap_result(true, '', id.st, 'expired', 0)
end

local function ap_quarantine(op)
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
    redis.call('HSET', base .. 'id:' .. s, 'st', 'quarantined', 'qu', sp_int_str(u), 'bu', '0', 'sct', sp_int_str(now))
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
    redis.call('HSET', pkey, 'st', 'quarantined', 'qu', sp_int_str(u), 'sct', sp_int_str(now))
    redis.call('ZREM', base .. 'pxrdy', s)
    return ap_result(true, '', p[1], 'quarantined', u)
  end
  return ap_result(false, 'invalid', '', '', 0)
end

local function ap_activate(op)
  local s = sp_str(op.s)
  local id = ap_id_get(s)
  if not id then
    return ap_result(false, 'missing', '', '', 0)
  end
  if id.st ~= 'pending' then
    return ap_result(false, 'not_pending', id.st, id.st, 0)
  end
  redis.call('HSET', base .. 'id:' .. s, 'st', 'active', 'act', sp_int_str(now), 'sct', sp_int_str(now))
  for _, eg in ipairs(op.egs) do
    sp_rescore(base, eg, s)
  end
  return ap_result(true, '', 'pending', 'active', 0)
end

local STATES = { pending = true, active = true, expired = true, banned = true, quarantined = true, disabled = true, retired = true }

local function ap_set(op)
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
    redis.call('HSET', akey, 'st', to, 'bu', sp_int_str(u), 'sct', sp_int_str(now))
    return ap_result(true, '', st, to, u)
  end
  if not STATES[to] then
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
  redis.call('HSET', key, 'st', to, 'bu', sp_int_str(bu), 'qu', sp_int_str(qu), 'sct', sp_int_str(now))
  if to == 'active' and id.st ~= 'active' then
    redis.call('HSET', key, 'act', sp_int_str(now))
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

local function ap_clear(op)
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
    sp_rescore(base, eg, s)
    redis.call('SADD', base .. 'dirty', 'e' .. eg .. ':' .. s)
    return ap_result(true, '', id.st, id.st, prev)
  elseif op.sc == 'is' then
    if id.scd <= now or (limit > 0 and id.scd > limit) then
      return ap_result(false, 'not_cooling', id.st, id.st, id.scd)
    end
    redis.call('HSET', base .. 'id:' .. s, 'scd', '0')
    ap_reset_identity(op, op.egs, s, fails, health)
    for _, eg in ipairs(op.egs) do
      sp_rescore(base, eg, s)
    end
    redis.call('SADD', base .. 'dirty', 'g' .. s)
    return ap_result(true, '', id.st, id.st, id.scd)
  end
  return ap_result(false, 'invalid', id.st, id.st, 0)
end

local function ap_reset_stats(op)
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

local HANDLERS = {
  cd = ap_cooldown, ban = ap_ban, exp = ap_expire, qua = ap_quarantine,
  act = ap_activate, set = ap_set, clr = ap_clear, rst = ap_reset_stats,
}

local out = {}
for idx = 3, #ARGV do
  local op = cjson.decode(ARGV[idx])
  op.egs = (type(op.egs) == 'table') and op.egs or {}
  op.add = (type(op.add) == 'table') and op.add or {}
  op.cnt = (type(op.cnt) == 'table') and op.cnt or {}
  local h = HANDLERS[op.op]
  if h then
    out[#out + 1] = h(op)
  else
    out[#out + 1] = ap_result(false, 'invalid', '', '', 0)
  end
end
if replay_key then
  redis.call('SET', replay_key, cjson.encode(out), 'PX', REPLAY_TTL_MS)
end
return out
