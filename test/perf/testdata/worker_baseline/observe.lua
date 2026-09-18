--[[
observe.lua (worker): apply one processed report to the hot state of a site.

KEYS[1] = P:T:meta (every other key is derived from the site base).

Common arguments:
  ARGV[1] mode        "full" | "ckpt"
  ARGV[2] shard       stream shard (field of P:T:ckpt)
  ARGV[3] stream id   "ms-seq" of the stream entry

Idempotency: P:T:ckpt[shard] holds the last applied stream id. An id that is
not greater (ms, then seq, compared numerically) returns {"DUP"} without any
write. Otherwise the checkpoint is advanced first, so a retried call never
applies the same entry twice. Mode "ckpt" only runs this step (reports whose
lease is gone are recorded for statistics only) and returns {"OK"}.

Mode "full" arguments (in order after ARGV[3]):
  now ms, lease id, eg hkey, identity hkey, proxy hkey ("" = none),
  outcome, blame (identity|proxy|both|none), late (0/1),
  identity observation value ("" = outcome not observed),
  proxy observation value ("" = outcome not observed),
  failure outcome (0/1, policy.IsFailureOutcome), risk outcome (0/1),
  health alpha, health baseline, health tau ms, failure_reset_after ms,
  breaker window ms, breaker bucket ms (0 = no window accounting),
  cross attribution enabled (0/1), cross window ms,
  proxy distinct identities threshold, identity distinct proxies threshold,
  Q, Q quota window ms values,
  C, C pairs (subject kind i|a|p, window ms) — counters of the current outcome,
  B, B escalation window ms values.

This is the frozen pre-optimization source used by BenchmarkABWorker; the
shipped script lives in internal/worker/lua/observe.lua.
]]

local function ob_stream_id(s)
  local dash = string.find(s, '-', 1, true)
  if not dash then
    return sp_num(s, 0), 0
  end
  return sp_num(string.sub(s, 1, dash - 1), 0), sp_num(string.sub(s, dash + 1), 0)
end

local ob_base = sp_base()
local ob_shard = ARGV[2]
local ob_sid = ARGV[3]
local ob_ckpt_key = ob_base .. 'ckpt'
local ob_prev = redis.call('HGET', ob_ckpt_key, ob_shard)
if ob_prev then
  local pms, pseq = ob_stream_id(ob_prev)
  local cms, cseq = ob_stream_id(ob_sid)
  if cms < pms or (cms == pms and cseq <= pseq) then
    return {'DUP'}
  end
end
redis.call('HSET', ob_ckpt_key, ob_shard, ob_sid)
if ARGV[1] ~= 'full' then
  return {'OK'}
end

local ob_pos = 3
local function ob_arg()
  ob_pos = ob_pos + 1
  local v = ARGV[ob_pos]
  if v == nil then
    error('observe: missing argument ' .. ob_pos)
  end
  return v
end
local function ob_numarg()
  return sp_num(ob_arg(), 0)
end

local now = ob_numarg()
local lease_id = ob_arg()
local eg = ob_arg()
local ident = ob_arg()
local proxy = ob_arg()
local outcome = ob_arg()
local blame = ob_arg()
local late = ob_arg() == '1'
local obs_identity = ob_arg()
local obs_proxy = ob_arg()
local failure = ob_arg() == '1'
local risk = ob_arg() == '1'
local alpha = ob_numarg()
local baseline = ob_numarg()
local tau = ob_numarg()
local reset_after = ob_numarg()
local win_ms = ob_numarg()
local bucket_ms = ob_numarg()
local xa_on = ob_arg() == '1'
local xa_window = ob_numarg()
local xa_proxy_ids = ob_numarg()
local xa_id_proxies = ob_numarg()
local quotas = {}
for k = 1, ob_numarg() do
  quotas[k] = ob_numarg()
end
local counters = {}
for k = 1, ob_numarg() do
  counters[k] = {ob_arg(), ob_numarg()}
end
local ban_windows = {}
for k = 1, ob_numarg() do
  ban_windows[k] = ob_numarg()
end

local is_success = outcome == 'success'
local dirty_key = ob_base .. 'dirty'

-- Lease report count and quota (the first request is counted at acquire).
local probe = false
local lease_key = ob_base .. 'ls:' .. lease_id
if redis.call('EXISTS', lease_key) == 1 then
  probe = redis.call('HGET', lease_key, 'pr') == '1'
  local rc = redis.call('HINCRBY', lease_key, 'rc', 1)
  if rc > 1 and #quotas > 0 and ident ~= '' then
    local qkey = ob_base .. 'q:' .. eg .. ':' .. ident
    local maxw = 0
    for _, w in ipairs(quotas) do
      if w > 0 then
        if w > maxw then maxw = w end
        local ws = sp_int_str(w)
        local idx = math.floor(now / w)
        local cur = redis.call('HMGET', qkey, ws .. ':c', ws .. ':n')
        local c = sp_num(cur[1], -1)
        if c == idx then
          redis.call('HINCRBY', qkey, ws .. ':n', 1)
        elseif c == idx - 1 then
          redis.call('HSET', qkey, ws .. ':c', sp_int_str(idx), ws .. ':p', sp_int_str(sp_num(cur[2], 0)), ws .. ':n', '1')
        else
          redis.call('HSET', qkey, ws .. ':c', sp_int_str(idx), ws .. ':p', '0', ws .. ':n', '1')
        end
      end
    end
    if maxw > 0 then
      redis.call('PEXPIRE', qkey, sp_int_str(2 * maxw))
    end
  end
end

-- Breaker window buckets and activity.
if bucket_ms > 0 and win_ms > 0 then
  local bucket = sp_int_str(math.floor(now / bucket_ms))
  local wkey = ob_base .. 'win:' .. eg .. ':' .. bucket
  redis.call('HINCRBY', wkey, 't', 1)
  if is_success then
    redis.call('HINCRBY', wkey, 's', 1)
  end
  if risk then
    redis.call('HINCRBY', wkey, 'r', 1)
  end
  redis.call('PEXPIRE', wkey, sp_int_str(2 * win_ms))
  if outcome == 'captcha' and ident ~= '' then
    local hkey = ob_base .. 'winh:' .. eg .. ':' .. bucket
    redis.call('PFADD', hkey, ident)
    redis.call('PEXPIRE', hkey, sp_int_str(2 * win_ms))
  end
end
redis.call('ZADD', ob_base .. 'aeg', sp_score_str(now), eg)

-- Breaker state, suppression and probe statistics.
local brk_key = ob_base .. 'brk:' .. eg
local brk = redis.call('HMGET', brk_key, 'st', 'ou', 'man')
local brk_state = brk[1]
if not brk_state or brk_state == '' then
  brk_state = 'closed'
end
local brk_ou = sp_num(brk[2], 0)
local brk_open = brk_state == 'open' and ((brk_ou == 0 and brk[3] == '1') or now < brk_ou)
local suppressed = brk_open and not probe
if probe and brk_state == 'half_open' then
  redis.call('HINCRBY', brk_key, 'ps', 1)
  if is_success then
    redis.call('HINCRBY', brk_key, 'pk', 1)
  end
end

-- Cross attribution (spec §6.5).
if risk and xa_on and xa_window > 0 and proxy ~= '' and ident ~= '' and not late then
  local cutoff = '(' .. sp_score_str(now - xa_window)
  local xp = ob_base .. 'xa:p:' .. proxy
  local xi = ob_base .. 'xa:i:' .. ident
  redis.call('ZADD', xp, sp_score_str(now), ident)
  redis.call('ZREMRANGEBYSCORE', xp, '-inf', cutoff)
  redis.call('PEXPIRE', xp, sp_int_str(xa_window))
  redis.call('ZADD', xi, sp_score_str(now), proxy)
  redis.call('ZREMRANGEBYSCORE', xi, '-inf', cutoff)
  redis.call('PEXPIRE', xi, sp_int_str(xa_window))
  local ids_on_proxy = redis.call('ZCARD', xp)
  local proxies_of_id = redis.call('ZCARD', xi)
  if ids_on_proxy >= xa_proxy_ids and proxies_of_id < xa_id_proxies then
    blame = 'proxy'
  elseif proxies_of_id >= xa_id_proxies then
    if blame == 'proxy' then
      blame = 'both'
    elseif blame ~= 'both' then
      blame = 'identity'
    end
  end
end
local blame_identity = blame == 'identity' or blame == 'both'
local blame_proxy = blame == 'proxy' or blame == 'both'

local id_key = ob_base .. 'id:' .. ident
local idv = redis.call('HMGET', id_key, 'st', 'acc', 'ty', 'gs', 'gts', 'gn', 'gnf', 'glf')
local id_state = idv[1] or ''
local account = idv[2] or ''
local id_type = idv[3] or ''

local e_nfail, g_nfail, p_nfail = 0, 0, 0
local e_score, g_score = baseline, baseline
local e_samples, g_samples = 0, 0
local e_cd = 0
local counts = {}
local bans = {}
for k = 1, #counters do counts[k] = 0 end
for k = 1, #ban_windows do bans[k] = 0 end

if not suppressed and not late and ident ~= '' and id_state ~= '' then
  local affects_identity = obs_identity ~= '' and (is_success or blame_identity)
  local obs_i = sp_num(obs_identity, 0)
  local fail_identity = failure or (outcome == 'network_error' and blame_identity)

  -- Identity x endpoint group health (packed hs entry).
  local hs = sp_hs_get(ob_base, eg, ident, baseline, now)
  local hs_changed = false
  local decayed = sp_decay(hs.score, hs.sts, now, baseline, tau)
  if affects_identity then
    hs.score = sp_ewma(decayed, obs_i, alpha)
    hs.sts = now
    hs.samples = hs.samples + 1
    decayed = hs.score
    hs_changed = true
  end
  if fail_identity then
    if now - hs.lastfail > reset_after then
      hs.nfail = 0
    end
    hs.nfail = hs.nfail + 1
    hs.lastfail = now
    hs_changed = true
  elseif is_success and hs.nfail > 0 then
    hs.nfail = math.floor(hs.nfail / 2)
    hs_changed = true
  end
  if hs_changed then
    sp_hs_set(ob_base, eg, ident, hs)
    redis.call('SADD', dirty_key, 'e' .. eg .. ':' .. ident)
  end
  e_score, e_samples, e_nfail, e_cd = decayed, hs.samples, hs.nfail, hs.cd

  -- Identity global score and streak.
  local gs = sp_num(idv[4], baseline)
  local gts = sp_num(idv[5], now)
  local gn = sp_num(idv[6], 0)
  local gnf = sp_num(idv[7], 0)
  local glf = sp_num(idv[8], 0)
  local gfields = {}
  g_score = sp_decay(gs, gts, now, baseline, tau)
  if affects_identity then
    g_score = sp_ewma(g_score, obs_i, alpha)
    gn = gn + 1
    table.insert(gfields, 'gs'); table.insert(gfields, string.format('%.2f', g_score))
    table.insert(gfields, 'gts'); table.insert(gfields, sp_int_str(now))
    table.insert(gfields, 'gn'); table.insert(gfields, sp_int_str(gn))
  end
  if fail_identity then
    if now - glf > reset_after then
      gnf = 0
    end
    gnf = gnf + 1
    table.insert(gfields, 'gnf'); table.insert(gfields, sp_int_str(gnf))
    table.insert(gfields, 'glf'); table.insert(gfields, sp_int_str(now))
  elseif is_success and gnf > 0 then
    gnf = math.floor(gnf / 2)
    table.insert(gfields, 'gnf'); table.insert(gfields, sp_int_str(gnf))
  end
  if #gfields > 0 then
    redis.call('HSET', id_key, unpack(gfields))
    redis.call('SADD', dirty_key, 'g' .. ident)
  end
  g_samples, g_nfail = gn, gnf

  -- Proxy x site health and streak.
  if proxy ~= '' then
    local px_key = ob_base .. 'px:' .. proxy
    if redis.call('EXISTS', px_key) == 1 then
      local pv = redis.call('HMGET', px_key, 'sc', 'sts', 'sn', 'nf', 'lf')
      local nf = sp_num(pv[4], 0)
      local pfields = {}
      if obs_proxy ~= '' and (is_success or blame_proxy) then
        local sc = sp_decay(sp_num(pv[1], baseline), sp_num(pv[2], now), now, baseline, tau)
        sc = sp_ewma(sc, sp_num(obs_proxy, 0), alpha)
        table.insert(pfields, 'sc'); table.insert(pfields, string.format('%.2f', sc))
        table.insert(pfields, 'sts'); table.insert(pfields, sp_int_str(now))
        table.insert(pfields, 'sn'); table.insert(pfields, sp_int_str(sp_num(pv[3], 0) + 1))
      end
      local fail_proxy = blame_proxy and not is_success and
        (failure or outcome == 'proxy_error' or outcome == 'network_error')
      if fail_proxy then
        if now - sp_num(pv[5], 0) > reset_after then
          nf = 0
        end
        nf = nf + 1
        table.insert(pfields, 'nf'); table.insert(pfields, sp_int_str(nf))
        table.insert(pfields, 'lf'); table.insert(pfields, sp_int_str(now))
      elseif is_success and nf > 0 then
        nf = math.floor(nf / 2)
        table.insert(pfields, 'nf'); table.insert(pfields, sp_int_str(nf))
      end
      if #pfields > 0 then
        redis.call('HSET', px_key, unpack(pfields))
        redis.call('SADD', dirty_key, 'p' .. proxy)
      end
      p_nfail = nf
    end
  end

  -- Sliding-window outcome counters.
  local counted = {}
  for k, c in ipairs(counters) do
    local kind, w = c[1], c[2]
    local subject = nil
    if kind == 'i' then
      subject = 'i' .. ident
    elseif kind == 'a' then
      if account ~= '' then subject = 'a' .. account else subject = 'i' .. ident end
    elseif kind == 'p' and proxy ~= '' then
      subject = 'p' .. proxy
    end
    if subject and w > 0 then
      local ckey = ob_base .. 'cnt:' .. subject .. ':' .. outcome .. ':' .. sp_int_str(w)
      if counted[ckey] then
        counts[k] = counted[ckey]
      else
        local bms = math.max(math.floor(w / 60), 1000)
        local idx = math.floor(now / bms)
        local lowest = idx - math.ceil(w / bms) + 1
        redis.call('HINCRBY', ckey, sp_int_str(idx), 1)
        local all = redis.call('HGETALL', ckey)
        local sum, stale = 0, {}
        for j = 1, #all, 2 do
          local bi = tonumber(all[j])
          if bi == nil or bi < lowest then
            stale[#stale + 1] = all[j]
          else
            sum = sum + sp_num(all[j + 1], 0)
          end
        end
        if #stale > 0 then
          redis.call('HDEL', ckey, unpack(stale))
        end
        redis.call('PEXPIRE', ckey, sp_int_str(w + bms))
        counted[ckey] = sum
        counts[k] = sum
      end
    end
  end

  -- Ban history per escalation window.
  local bans_key = ob_base .. 'bans:' .. ident
  for k, w in ipairs(ban_windows) do
    bans[k] = redis.call('ZCOUNT', bans_key, sp_score_str(now - w), '+inf')
  end
end

return {
  'OK', blame, suppressed and 1 or 0, probe and 1 or 0, id_state, account, id_type,
  e_nfail, g_nfail, p_nfail,
  string.format('%.4f', e_score), e_samples, string.format('%.4f', g_score), g_samples,
  sp_int_str(e_cd), brk_state, counts, bans,
}
