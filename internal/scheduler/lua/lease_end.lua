--[[
lease_end.lua (owner: internal/scheduler) - shared prelude of release.lua and
reap.lua. It is concatenated in front of those bodies (after common.lua).

Endpoint group layout parsed by ls_groups (ARGV starting at a position):
  n clients, then one entry per client holding that client's
  "group hkey,health baseline" pairs separated by commas.
]]

local LS_ST, LS_I, LS_E, LS_P, LS_PID, LS_IID, LS_N, LS_TK, LS_PR, LS_MC, LS_RI, LS_RA, LS_RS, LS_X, LS_NS, LS_RC =
  1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16
local LS_A, LS_PRU, LS_KX, LS_RV = 17, 18, 19, 20

-- ls_read reads the lease hash in the field order the LS_* indexes above name
-- (spec §5.3, written by acquire_write.lua). The names are passed inline: a
-- 20-entry table plus unpack() costs 0.7 us, which reap.lua would pay once per
-- expired lease.
local function ls_read(base, lid)
  return redis.call('HMGET', base .. 'ls:' .. lid,
    'st', 'i', 'e', 'p', 'pid', 'iid', 'n', 'tk', 'pr', 'mc', 'ri', 'ra', 'rs', 'x', 'ns', 'rc',
    'a', 'pru', 'kx', 'rv')
end

-- Endpoint group layout state. ls_layout_at records where the layout starts in
-- ARGV; the layout itself is parsed at most once per script call, and only when
-- a lease end really has to walk the other groups of the client, which the "xg"
-- marker makes unnecessary for the common lease end.
local ls_pos = 1
local ls_groups_v, ls_baselines_v = nil, nil

-- HS_NO_BASELINE is handed to sp_hs_unpack in place of the health baseline. It
-- comes back as the score exactly when the stored entry has no usable score
-- field, which is the only case in which the baseline is read at all, so the
-- layout is not parsed for a lease whose packed state is intact - which is
-- every lease acquire wrote. No real score can equal it.
local HS_NO_BASELINE = -1e300

local function ls_layout_at(pos)
  ls_pos = pos
end

-- ls_groups parses the endpoint group layout. It returns (groups, baselines)
-- where groups maps a group hkey to the list of hkeys of its client and
-- baselines maps a group hkey to its health baseline.
local function ls_groups()
  if ls_groups_v then
    return ls_groups_v, ls_baselines_v
  end
  local groups, baselines = {}, {}
  local pos = ls_pos
  local nclients = tonumber(ARGV[pos]) or 0
  for ci = 1, nclients do
    local parts = sp_split(ARGV[pos + ci], ',')
    local list, n = {}, 0
    for k = 1, #parts - 1, 2 do
      n = n + 1
      list[n] = parts[k]
      baselines[parts[k]] = tonumber(parts[k + 1]) or 0
    end
    for _, g in ipairs(list) do
      groups[g] = list
    end
  end
  ls_groups_v, ls_baselines_v = groups, baselines
  return groups, baselines
end

-- ls_baseline returns the health baseline of one endpoint group. It is only
-- reached when the packed state of the identity has no usable score, which is
-- never the case for an entry acquire wrote, so parsing the whole layout here
-- is cheaper than keeping a second reader for it.
local function ls_baseline(eg)
  local _, baselines = ls_groups()
  return baselines[eg] or 0
end

-- ls_abort_accounting rolls back the quota count and the half-open probe slot
-- that acquire charged to a lease that was never delivered. windows lists the
-- quota window lengths (ms) of the rotation policy.
local function ls_abort_accounting(base, v, windows)
  local i, eg = v[LS_I] or '', v[LS_E] or ''
  local a = sp_num(v[LS_A], 0)
  if i ~= '' and a > 0 and #windows > 0 then
    local qkey = base .. 'q:' .. eg .. ':' .. i
    for _, w in ipairs(windows) do
      local q = redis.call('HMGET', qkey, w .. ':c', w .. ':n')
      local n = sp_num(q[2], 0)
      if q[1] and sp_num(q[1], -1) == math.floor(a / tonumber(w)) and n > 0 then
        redis.call('HSET', qkey, w .. ':n', n - 1)
      end
    end
  end
  if v[LS_PR] == '1' then
    local brk = base .. 'brk:' .. eg
    local b = redis.call('HMGET', brk, 'st', 'hw')
    if b[1] == 'half_open' and sp_num(b[2], 0) <= a then
      if redis.call('HINCRBY', brk, 'hc', -1) < 0 then
        redis.call('HSET', brk, 'hc', '0')
      end
    end
  end
end

-- ls_end ends the active lease lid whose ls_read values are v:
-- sets st/end, drops it from lsexp, keeps the hash for the late report
-- window (or longer while ingested reports may still be queued: "kx"),
-- decrements identity and proxy concurrency, applies a "released" reuse
-- anchor from end_ms and rescores the identity (spec §6.1, §5.6). The endpoint
-- group layout is read from ARGV through ls_baseline/ls_groups; the caller only
-- has to declare its position with ls_layout_at.
-- abort_windows is nil for a lease that was delivered. For a lease that was
-- never delivered (rendering failed or the request was canceled) it lists the
-- quota window lengths of the rotation policy: no reuse anchor is applied, an
-- "acquired" anchor raised by this lease is restored ("pru"; for the site
-- scope also in the ready sets of the other groups) and the quota count and
-- half-open probe slot of the lease are rolled back.
local function ls_end(base, lid, v, new_state, end_ms, now, late, abort_windows)
  local lkey = base .. 'ls:' .. lid
  local abort = abort_windows ~= nil
  redis.call('HSET', lkey, 'st', new_state, 'end', end_ms)
  local keep = sp_num(late, 0)
  local kx = sp_num(v[LS_KX], 0)
  if kx - now > keep then
    keep = kx - now
  end
  redis.call('PEXPIRE', lkey, keep)
  redis.call('ZREM', base .. 'lsexp', lid)
  if abort then
    ls_abort_accounting(base, v, abort_windows)
  end

  local i, eg, p = v[LS_I] or '', v[LS_E] or '', v[LS_P] or ''
  local idkey = base .. 'id:' .. i
  local id = redis.call('HMGET', idkey, 'st', 'al', 'xl', 'scd', 'sru', 'acc', 'xg')
  if i ~= '' and id[1] then
    local al = sp_num(id[2], 0) - 1
    if al < 0 then
      al = 0
    end
    local oldxl = sp_num(id[3], 0)
    local xl = oldxl
    local upd = {'al', al}
    if al == 0 then
      xl = 0
      upd[#upd + 1] = 'xl'
      upd[#upd + 1] = '0'
    end
    local ri = sp_num(v[LS_RI], 0)
    local anchor = not abort and v[LS_RA] == 'r' and ri > 0
    local site_scope = v[LS_RS] == 's'
    -- restore: the reuse value an aborted lease raised at acquire, unless a
    -- later lease raised it again.
    local restore = abort and v[LS_RA] == 'a' and ri > 0 and v[LS_PRU]
    local raised = sp_num(v[LS_A], 0) + ri
    local sru = sp_num(id[5], 0)
    local restored_site = false
    if anchor and site_scope and end_ms + ri > sru then
      sru = end_ms + ri
      upd[#upd + 1] = 'sru'
      upd[#upd + 1] = sru
      redis.call('SADD', base .. 'dirty', 'g' .. i)
    elseif restore and site_scope and sru == raised then
      sru = sp_num(v[LS_PRU], 0)
      restored_site = true
      upd[#upd + 1] = 'sru'
      upd[#upd + 1] = sru
      redis.call('SADD', base .. 'dirty', 'g' .. i)
    end
    redis.call('HSET', idkey, unpack(upd))

    -- The packed state is only decoded in full when it is going to be written
    -- back, which happens for an endpoint-scope reuse anchor and for the
    -- restore of an aborted one. The default rotation policy has a reuse
    -- interval of 0, so the usual lease end reads the two fields it needs and
    -- nothing else.
    local hsraw = redis.call('HGET', base .. 'hs:' .. eg, i)
    local hs_cd, hs_ru
    if (anchor or restore) and not site_scope then
      local hs = sp_hs_unpack(hsraw, HS_NO_BASELINE, now)
      if hs.score == HS_NO_BASELINE then
        hs.score = ls_baseline(eg)
      end
      hs_cd, hs_ru = hs.cd, hs.ru
      if anchor and end_ms + ri > hs.ru then
        hs.ru = end_ms + ri
        sp_hs_set(base, eg, i, hs)
        hs_ru = hs.ru
        redis.call('SADD', base .. 'dirty', 'e' .. eg .. ':' .. i)
      elseif restore and hsraw and hs.ru == raised then
        hs.ru = sp_num(v[LS_PRU], 0)
        sp_hs_set(base, eg, i, hs)
        hs_ru = hs.ru
        redis.call('SADD', base .. 'dirty', 'e' .. eg .. ':' .. i)
      end
    else
      hs_cd, hs_ru = sp_hs_cd_ru(hsraw)
    end

    local idlevel = sp_num(id[4], 0)
    if sru > idlevel then idlevel = sru end
    if al > 0 and xl > idlevel then idlevel = xl end
    if id[6] and id[6] ~= '' then
      local acd = sp_num(redis.call('HGET', base .. 'acc:' .. id[6], 'cd'), 0)
      if acd > idlevel then idlevel = acd end
    end

    local sc = math.max(hs_cd, hs_ru, idlevel, end_ms)
    redis.call('ZADD', base .. 'rdy:' .. eg, 'XX', sc, i)

    -- Other groups of the client that pushed the identity to its exclusive
    -- lease expiry while it was leased get their availability back. Only an
    -- acquire that really pushed the identity sets "xg" (acquire_main.lua), so
    -- the common case (nobody sampled the identity while it was leased) costs
    -- nothing instead of one ZSCORE per endpoint group of the client. A marker
    -- lost with the identity hash (hot-state rebuild) only leaves the pushed
    -- score in place until it becomes due again, at most one lease TTL.
    local xgmark = id[7]
    local pushed = xgmark ~= nil and xgmark ~= false and xgmark ~= '' and xgmark ~= '0'
    if pushed and al == 0 then
      redis.call('HDEL', idkey, 'xg')
    end
    if pushed and sp_num(v[LS_MC], 0) == 1 and oldxl > 0 and al == 0 then
      -- The groups to restore are the ones that pushed the identity (spec
      -- §5.2), intersected with the groups of this lease's client, which are
      -- the only ones the rule covers. A bare "1" is the marker an older
      -- release wrote, which does not say which groups pushed: fall back to
      -- every group of the client.
      local list = (ls_groups())[eg]
      if list and xgmark ~= '1' then
        local own = {}
        for _, g in ipairs(list) do
          own[g] = true
        end
        local marked = sp_split(string.sub(xgmark, 2, -2), ',')
        list = {}
        for _, g in ipairs(marked) do
          if own[g] then
            list[#list + 1] = g
          end
        end
      end
      if list then
        for _, g in ipairs(list) do
          if g ~= eg then
            local zkey = base .. 'rdy:' .. g
            local s = redis.call('ZSCORE', zkey, i)
            if s then
              s = tonumber(s)
              if s > end_ms and s <= oldxl then
                local c2, r2 = sp_hs_cd_ru(redis.call('HGET', base .. 'hs:' .. g, i))
                local t = math.max(c2, r2, idlevel, end_ms)
                if t < s then
                  redis.call('ZADD', zkey, 'XX', t, i)
                end
              end
            end
          end
        end
      end
    end

    -- The site reuse value applies to every group of the site: acquires in
    -- other groups (of any client) that sampled the identity while the
    -- aborted lease was being rendered pushed it up to the raised value.
    -- Pull such scores back; a score that ends up too early is only
    -- re-evaluated by the next acquire that samples it.
    if restored_site then
      for g in pairs((ls_groups())) do
        if g ~= eg then
          local zkey = base .. 'rdy:' .. g
          local s = redis.call('ZSCORE', zkey, i)
          if s then
            s = tonumber(s)
            if s > end_ms and s <= raised then
              local c2, r2 = sp_hs_cd_ru(redis.call('HGET', base .. 'hs:' .. g, i))
              local t = math.max(c2, r2, idlevel, end_ms)
              if t < s then
                redis.call('ZADD', zkey, 'XX', t, i)
              end
            end
          end
        end
      end
    end
  end

  if p ~= '' then
    local pkey = base .. 'px:' .. p
    local ph = redis.call('HMGET', pkey, 'st', 'al', 'mc', 'cd', 'gcd')
    if ph[1] then
      local pal = sp_num(ph[2], 0) - 1
      if pal < 0 then
        pal = 0
      end
      redis.call('HSET', pkey, 'al', pal)
      if pal < sp_num(ph[3], 1) then
        local zkey = base .. 'pxrdy'
        local cur = redis.call('ZSCORE', zkey, p)
        if cur then
          local t = math.max(sp_num(ph[4], 0), sp_num(ph[5], 0), now)
          if tonumber(cur) > t then
            redis.call('ZADD', zkey, 'XX', t, p)
          end
        end
      end
    end
  end
end
