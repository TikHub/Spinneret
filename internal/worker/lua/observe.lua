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

Mode "full" arguments, at fixed positions (ARGV[4] onwards):
   4 now ms                        12 identity observation ("" = not observed)
   5 lease id                      13 proxy observation ("" = not observed)
   6 eg hkey                       14 failure outcome (0/1)
   7 identity hkey                 15 risk outcome (0/1)
   8 proxy hkey ("" = none)        16 health policy (packed, see below)
   9 outcome                       17 breaker window suffix "<eg>:<bucket>"
  10 blame                         18 breaker window key ttl ms
  11 late (0/1)                    19 cross attribution (packed, see below)
Packed 16 = "alpha,baseline,tau_ms,failure_reset_after_ms".
Packed 19 = "enabled(0/1),window_ms,proxy_distinct_identities,identity_distinct_proxies";
it is parsed only for a risk outcome that can be cross attributed at all.
Suffix 17 is "" when the policy keeps no breaker window; Go computes the bucket
index (floor(now / bucket_ms)) and the ttl (2 x window) so that the script needs
neither the bucket length nor a "%.14g" of the index in a key.
Then three counted lists, each introduced by its length:
  20        Q, then Q quota window ms values
  21+Q      C, then C pairs (subject kind i|a|p, window ms) of the outcome
  22+Q+2C   B, then B escalation window ms values

Effects (spec §5, §6.3-§6.5):
  * lease "rc"++ (only while the lease hash exists); when rc > 1 the quota
    windows of q:<eg>:<i> advance ("<w>:c" = floor(now/w) window index,
    "<w>:n" current count, "<w>:p" previous count; PX 2 x max window).
  * breaker window win:<eg>:<floor(now/bucket)>: t++, s++ on success, r++ on a
    risk outcome (PX 2 x window, set when the bucket is created); winh:<eg>:
    <bucket> PFADD identity on captcha; aeg ZADD eg now.
  * breaker brk:<eg> "st"/"ou"/"man" read: suppressed = the breaker is
    effectively open (st == "open" and (now < ou or a manual open without end))
    and the lease is not a probe; probe leases (pr=1) on a half_open breaker
    increment "ps" (and "pk" on success).
  * cross attribution (risk outcome with a proxy, not late): xa:p:<p> and
    xa:i:<i> ZSETs trimmed to the window (PX window); >= proxy threshold
    identities on the proxy and < identity threshold proxies on the identity
    blame the proxy; >= identity threshold proxies adds the identity to the
    blame.
  Unless suppressed or late, and only while id:<i> exists:
  * hs:<eg>[i]: decay + EWMA with the identity observation when it affects the
    identity (success, or blame includes the identity), samples++; streak:
    failure outcomes (and network_error blamed on the identity) set
    nfail = (now - lastfail > reset ? 0 : nfail) + 1, lastfail = now; success
    halves nfail. cd/ru/lu are left unchanged. Dirty "e<eg>:<i>".
  * id:<i> global score gs/gts/gn with the same observation, and the global
    failure streak "gnf" / last global failure "glf" (worker-owned fields with
    the same streak rules; they approximate the identity x site streak).
    Dirty "g<i>".
  * px:<p> (when it exists): sc/sts/sn with the proxy observation when it
    affects the proxy (success, or blame includes the proxy); streak nf/lf on
    proxy_error, network_error and failure outcomes blamed on the proxy;
    success halves nf. Dirty "p<p>".
  * counters cnt:<subj>:<outcome>:<w> (bucket = max(floor(w/60), 1000) ms,
    field = bucket index, stale buckets deleted, PX w + bucket); subject
    "i<i>", "a<acc>" (identity key when the identity has no account) or
    "p<p>" (count 0 without a proxy).
  * ban counts: ZCOUNT bans:<i> [now - w, +inf] per escalation window.

Returns {"DUP"} or {"OK"} (mode ckpt) or
  {"OK", blame, suppressed, probe, identity state, account hkey, identity type,
   endpoint streak, global streak, proxy streak, endpoint score, endpoint
   samples, global score, global samples, endpoint cooldown until ms,
   breaker state, {counts...}, {ban counts...}}
Scores are strings with 4 decimals; missing identities yield "" states.

Hot-path conventions (spec §5.7): arguments are read from ARGV by index, not
through an accessor; constants of the compiled policy travel packed in one
entry and the cross-attribution entry is parsed only when it can matter;
integral redis.call arguments travel as Lua numbers, which the server renders
as exactly their digits; the packed hs entry is spliced with sp_hs_raw, so a
field this script does not change is never parsed nor re-formatted and the
millisecond timestamps it does change are spliced in as ARGV[4] itself; and the
decay and EWMA of spec §6.4 are written out here rather than called through
sp_decay and sp_ewma, whose argument coercion is redundant once the inputs are
numbers.
]]

local A = ARGV
local ob_base = sp_base()
local ob_shard = A[2]
local ob_sid = A[3]
local ob_ckpt_key = ob_base .. 'ckpt'
local ob_prev = redis.call('HGET', ob_ckpt_key, ob_shard)
if ob_prev then
  -- Stream ids are canonical "<ms>-<seq>" decimal numerals (no sign, no
  -- leading zeros), so the longer numeral is the larger one and equal lengths
  -- compare byte-wise. That avoids four tonumber calls on 13-digit numerals,
  -- which Lua 5.1 parses eight times slower than short ones.
  local pms, pseq = string.match(ob_prev, '^(%d+)%-(%d+)$')
  local cms, cseq = string.match(ob_sid, '^(%d+)%-(%d+)$')
  local dup
  if pms == nil or cms == nil then
    -- One side is not a canonical pair: compare numerically, reading the part
    -- before the first "-" as the ms and the rest (or 0) as the sequence.
    local pm, ps = string.match(ob_prev, '^([^-]*)%-?(.*)$')
    local cm, cs = string.match(ob_sid, '^([^-]*)%-?(.*)$')
    local pmn, cmn = sp_num(pm, 0), sp_num(cm, 0)
    dup = cmn < pmn or (cmn == pmn and sp_num(cs, 0) <= sp_num(ps, 0))
  elseif pms == cms then
    dup = #cseq < #pseq or (#cseq == #pseq and cseq <= pseq)
  else
    dup = #cms < #pms or (#cms == #pms and cms < pms)
  end
  if dup then
    return {'DUP'}
  end
end
redis.call('HSET', ob_ckpt_key, ob_shard, ob_sid)
if A[1] ~= 'full' then
  return {'OK'}
end
if #A < 22 then
  error('observe: full mode needs at least 22 arguments, got ' .. #A)
end

local now_s = A[4]
local now = sp_num(now_s, 0)
local lease_id = A[5]
local eg = A[6]
local ident = A[7]
local proxy = A[8]
local outcome = A[9]
local blame = A[10]
local late = A[11] == '1'
local obs_identity = A[12]
local obs_proxy = A[13]
local failure = A[14] == '1'
local risk = A[15] == '1'
local win_suffix = A[17]
local alpha_s, baseline_s, tau_s, reset_s = string.match(A[16], '^([^,]*),([^,]*),([^,]*),([^,]*)$')
if alpha_s == nil then
  error('observe: ARGV[16] must be "alpha,baseline,tau,failure_reset_after"')
end
local alpha = sp_num(alpha_s, 0)
local baseline = sp_num(baseline_s, 0)
local tau = tonumber(tau_s) or 0
local reset_after = tonumber(reset_s) or 0
if alpha < 0 then
  alpha = 0
elseif alpha > 1 then
  alpha = 1
end

-- The three counted lists stay in ARGV; their entries are read where they are
-- used, so a report whose policy asks for none allocates no table at all.
local qpos = 20
local nq = tonumber(A[qpos]) or 0
local cpos = qpos + nq + 1
local nc = tonumber(A[cpos]) or 0
local bpos = cpos + 2 * nc + 1
local nb = tonumber(A[bpos]) or 0

local is_success = outcome == 'success'

-- Lease report count and quota (the first request is counted at acquire).
-- "st" witnesses the hash: every lease acquire.lua writes carries it, so the
-- HMGET replaces an EXISTS and a separate HGET of "pr".
local probe = false
local lease_key = ob_base .. 'ls:' .. lease_id
local lease = redis.call('HMGET', lease_key, 'pr', 'st')
if lease[1] or lease[2] then
  probe = lease[1] == '1'
  local rc = redis.call('HINCRBY', lease_key, 'rc', 1)
  if rc > 1 and nq > 0 and ident ~= '' then
    local qkey = ob_base .. 'q:' .. eg .. ':' .. ident
    local maxw = 0
    for k = 1, nq do
      -- Go sends the window as a canonical integer, which is also the field
      -- prefix the scheduler writes, so no reformatting is needed.
      local ws = A[qpos + k]
      local w = tonumber(ws) or 0
      if w > 0 then
        if w > maxw then maxw = w end
        local idx = math.floor(now / w)
        local cur = redis.call('HMGET', qkey, ws .. ':c', ws .. ':n')
        local c = sp_num(cur[1], -1)
        if c == idx then
          redis.call('HINCRBY', qkey, ws .. ':n', 1)
        elseif c == idx - 1 then
          redis.call('HSET', qkey, ws .. ':c', idx, ws .. ':p', math.floor(sp_num(cur[2], 0)), ws .. ':n', 1)
        else
          redis.call('HSET', qkey, ws .. ':c', idx, ws .. ':p', 0, ws .. ':n', 1)
        end
      end
    end
    if maxw > 0 then
      redis.call('PEXPIRE', qkey, 2 * maxw)
    end
  end
end

-- Breaker window buckets and activity. The bucket key expires 2 x window after
-- it was created; only this script writes it, and it is summed for at most one
-- window after its start, so refreshing the TTL on every report is wasted work.
if win_suffix ~= '' then
  local wkey = ob_base .. 'win:' .. win_suffix
  local total = redis.call('HINCRBY', wkey, 't', 1)
  if is_success then
    redis.call('HINCRBY', wkey, 's', 1)
  end
  if risk then
    redis.call('HINCRBY', wkey, 'r', 1)
  end
  local win_ttl = tonumber(A[18]) or 0
  if total == 1 then
    redis.call('PEXPIRE', wkey, win_ttl)
  end
  if outcome == 'captcha' and ident ~= '' then
    local hkey = ob_base .. 'winh:' .. win_suffix
    redis.call('PFADD', hkey, ident)
    redis.call('PEXPIRE', hkey, win_ttl)
  end
end
redis.call('ZADD', ob_base .. 'aeg', now, eg)

-- Breaker state, suppression and probe statistics. An open breaker only
-- suppresses while it is effectively open, with the gate rule of acquire.lua
-- (spec §6.1): a manual open without end (man=1, ou=0) or now < ou. An expired
-- open breaker is half-open for acquire even before a script records it.
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

-- Cross attribution (spec §6.5). The packed policy is decoded only for a risk
-- outcome that has both subjects and is not late; every other report skips it.
if risk and proxy ~= '' and ident ~= '' and not late then
  local xa_on, xa_win_s, xa_pi, xa_ip = string.match(A[19], '^([^,]*),([^,]*),([^,]*),([^,]*)$')
  local xa_window = tonumber(xa_win_s) or 0
  if xa_on == '1' and xa_window > 0 then
    local cutoff = '(' .. (now - xa_window)
    local xp = ob_base .. 'xa:p:' .. proxy
    local xi = ob_base .. 'xa:i:' .. ident
    redis.call('ZADD', xp, now, ident)
    redis.call('ZREMRANGEBYSCORE', xp, '-inf', cutoff)
    redis.call('PEXPIRE', xp, xa_window)
    redis.call('ZADD', xi, now, proxy)
    redis.call('ZREMRANGEBYSCORE', xi, '-inf', cutoff)
    redis.call('PEXPIRE', xi, xa_window)
    local ids_on_proxy = redis.call('ZCARD', xp)
    local proxies_of_id = redis.call('ZCARD', xi)
    if ids_on_proxy >= (tonumber(xa_pi) or 0) and proxies_of_id < (tonumber(xa_ip) or 0) then
      blame = 'proxy'
    elseif proxies_of_id >= (tonumber(xa_ip) or 0) then
      if blame == 'proxy' then
        blame = 'both'
      elseif blame ~= 'both' then
        blame = 'identity'
      end
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
local e_cd = '0'
local counts = {}
local bans = {}
for k = 1, nc do counts[k] = 0 end
for k = 1, nb do bans[k] = 0 end

if not suppressed and not late and ident ~= '' and id_state ~= '' then
  local dirty_key = ob_base .. 'dirty'
  local dirty_e, dirty_g
  local affects_identity = obs_identity ~= '' and (is_success or blame_identity)
  local obs_i = sp_num(obs_identity, 0)
  local fail_identity = failure or (outcome == 'network_error' and blame_identity)

  -- Identity x endpoint group health (packed hs entry). Fields this report
  -- does not change travel back as the strings they came in as, and the two
  -- timestamps it does change are ARGV[4] itself, so the rewrite formats at
  -- most the score, the sample count and the failure streak.
  local hs_key = ob_base .. 'hs:' .. eg
  local raw = redis.call('HGET', hs_key, ident)
  local f1, f2, f3, f4, f5, cd_s, ru_s, lu_s = sp_hs_raw(raw)
  if f1 == nil then
    -- Missing entry, or a short form written by an older release: normalize it
    -- to the eight canonical fields and continue on the ordinary path.
    local t = sp_hs_unpack(raw, baseline, now)
    f1 = string.format('%.2f', t.score)
    if f1 == '-0.00' then
      f1 = '0.00'
    end
    f2, f3, f4, f5 = sp_int_str(t.sts), sp_int_str(t.samples), sp_int_str(t.nfail), sp_int_str(t.lastfail)
    cd_s, ru_s, lu_s = sp_int_str(t.cd), sp_int_str(t.ru), sp_int_str(t.lu)
  end
  local score = sp_num(f1, baseline)
  local samples = math.floor(sp_num(f3, 0))
  local nfail = math.floor(sp_num(f4, 0))
  local decayed = score
  local dt = now - sp_num(f2, now)
  if tau > 0 and dt > 0 then
    decayed = baseline + (score - baseline) * math.exp(-dt / tau)
  end
  -- w1..w5 are the five leading fields as they will be written back.
  local w1, w2, w3, w4, w5 = f1, f2, f3, f4, f5
  local hs_changed = false
  if affects_identity then
    score = alpha * obs_i + (1 - alpha) * decayed
    decayed = score
    samples = samples + 1
    w1 = string.format('%.2f', score)
    if w1 == '-0.00' then
      w1 = '0.00'
    end
    w2, w3 = now_s, samples
    hs_changed = true
  end
  if fail_identity then
    if now - sp_num(f5, 0) > reset_after then
      nfail = 0
    end
    nfail = nfail + 1
    w4, w5 = nfail, now_s
    hs_changed = true
  elseif is_success and nfail > 0 then
    nfail = math.floor(nfail / 2)
    w4 = nfail
    hs_changed = true
  end
  if hs_changed then
    redis.call('HSET', hs_key, ident,
      w1 .. '|' .. w2 .. '|' .. w3 .. '|' .. w4 .. '|' .. w5 .. '|' .. cd_s .. '|' .. ru_s .. '|' .. lu_s)
    dirty_e = 'e' .. eg .. ':' .. ident
  end
  e_score, e_samples, e_nfail = decayed, samples, nfail
  -- "cd" is a canonical integer in every entry this system writes; only a
  -- value that is not has to go through the numeric normalization.
  if string.find(cd_s, '^%d+$') then
    e_cd = cd_s
  else
    e_cd = sp_int_str(sp_num(cd_s, 0))
  end

  -- Identity global score and streak.
  local gn = sp_num(idv[6], 0)
  local gnf = sp_num(idv[7], 0)
  g_score = sp_num(idv[4], baseline)
  local gdt = now - sp_num(idv[5], now)
  if tau > 0 and gdt > 0 then
    g_score = baseline + (g_score - baseline) * math.exp(-gdt / tau)
  end
  local gs_s, gnf_set, glf_set
  if affects_identity then
    g_score = alpha * obs_i + (1 - alpha) * g_score
    gn = gn + 1
    gs_s = string.format('%.2f', g_score)
  end
  if fail_identity then
    if now - sp_num(idv[8], 0) > reset_after then
      gnf = 0
    end
    gnf = gnf + 1
    gnf_set, glf_set = true, true
  elseif is_success and gnf > 0 then
    gnf = math.floor(gnf / 2)
    gnf_set = true
  end
  if gs_s then
    if glf_set then
      redis.call('HSET', id_key, 'gs', gs_s, 'gts', now, 'gn', gn, 'gnf', gnf, 'glf', now)
    elseif gnf_set then
      redis.call('HSET', id_key, 'gs', gs_s, 'gts', now, 'gn', gn, 'gnf', gnf)
    else
      redis.call('HSET', id_key, 'gs', gs_s, 'gts', now, 'gn', gn)
    end
    dirty_g = 'g' .. ident
  elseif glf_set then
    redis.call('HSET', id_key, 'gnf', gnf, 'glf', now)
    dirty_g = 'g' .. ident
  elseif gnf_set then
    redis.call('HSET', id_key, 'gnf', gnf)
    dirty_g = 'g' .. ident
  end
  g_samples, g_nfail = gn, gnf

  -- One SADD for both entries: most of the 0.7 us of a SADD is the call.
  if dirty_e then
    if dirty_g then
      redis.call('SADD', dirty_key, dirty_e, dirty_g)
    else
      redis.call('SADD', dirty_key, dirty_e)
    end
  elseif dirty_g then
    redis.call('SADD', dirty_key, dirty_g)
  end

  -- Proxy x site health and streak. "st" witnesses the hash: every score field
  -- below is optional, the state is not.
  if proxy ~= '' then
    local px_key = ob_base .. 'px:' .. proxy
    local pv = redis.call('HMGET', px_key, 'sc', 'sts', 'sn', 'nf', 'lf', 'st')
    if pv[6] then
      local nf = sp_num(pv[4], 0)
      local sc_s, sn
      if obs_proxy ~= '' and (is_success or blame_proxy) then
        local sc = sp_num(pv[1], baseline)
        local pdt = now - sp_num(pv[2], now)
        if tau > 0 and pdt > 0 then
          sc = baseline + (sc - baseline) * math.exp(-pdt / tau)
        end
        sc_s = string.format('%.2f', alpha * sp_num(obs_proxy, 0) + (1 - alpha) * sc)
        sn = math.floor(sp_num(pv[3], 0)) + 1
      end
      local nf_set, lf_set
      if blame_proxy and not is_success and
        (failure or outcome == 'proxy_error' or outcome == 'network_error') then
        if now - sp_num(pv[5], 0) > reset_after then
          nf = 0
        end
        nf = nf + 1
        nf_set, lf_set = true, true
      elseif is_success and nf > 0 then
        nf = math.floor(nf / 2)
        nf_set = true
      end
      if sc_s then
        if lf_set then
          redis.call('HSET', px_key, 'sc', sc_s, 'sts', now, 'sn', sn, 'nf', nf, 'lf', now)
        elseif nf_set then
          redis.call('HSET', px_key, 'sc', sc_s, 'sts', now, 'sn', sn, 'nf', nf)
        else
          redis.call('HSET', px_key, 'sc', sc_s, 'sts', now, 'sn', sn)
        end
      elseif lf_set then
        redis.call('HSET', px_key, 'nf', nf, 'lf', now)
      elseif nf_set then
        redis.call('HSET', px_key, 'nf', nf)
      end
      if sc_s or nf_set then
        redis.call('SADD', dirty_key, 'p' .. proxy)
      end
      p_nfail = nf
    end
  end

  -- Sliding-window outcome counters.
  if nc > 0 then
    local counted = {}
    for k = 1, nc do
      local kind = A[cpos + 2 * k - 1]
      local ws = A[cpos + 2 * k]
      local w = tonumber(ws) or 0
      local subject
      if kind == 'i' then
        subject = 'i' .. ident
      elseif kind == 'a' then
        if account ~= '' then subject = 'a' .. account else subject = 'i' .. ident end
      elseif kind == 'p' and proxy ~= '' then
        subject = 'p' .. proxy
      end
      if subject and w > 0 then
        local ckey = ob_base .. 'cnt:' .. subject .. ':' .. outcome .. ':' .. ws
        if counted[ckey] then
          counts[k] = counted[ckey]
        else
          local bms = math.max(math.floor(w / 60), 1000)
          local idx = math.floor(now / bms)
          local lowest = idx - math.ceil(w / bms) + 1
          redis.call('HINCRBY', ckey, idx, 1)
          local all = redis.call('HGETALL', ckey)
          local sum, stale, ns = 0, nil, 0
          for j = 1, #all, 2 do
            local bi = tonumber(all[j])
            if bi == nil or bi < lowest then
              ns = ns + 1
              if stale then stale[ns] = all[j] else stale = {all[j]} end
            else
              sum = sum + (tonumber(all[j + 1]) or 0)
            end
          end
          if stale then
            redis.call('HDEL', ckey, unpack(stale))
          end
          redis.call('PEXPIRE', ckey, w + bms)
          counted[ckey] = sum
          counts[k] = sum
        end
      end
    end
  end

  -- Ban history per escalation window.
  if nb > 0 then
    local bans_key = ob_base .. 'bans:' .. ident
    for k = 1, nb do
      bans[k] = redis.call('ZCOUNT', bans_key, now - (tonumber(A[bpos + k]) or 0), '+inf')
    end
  end
end

return {
  'OK', blame, suppressed and 1 or 0, probe and 1 or 0, id_state, account, id_type,
  e_nfail, g_nfail, p_nfail,
  string.format('%.4f', e_score), e_samples, string.format('%.4f', g_score), g_samples,
  e_cd, brk_state, counts, bans,
}
