-- acquire_filters.lua: candidate filters of acquire.lua (spec §6.1 step 3).

-- account_avail folds the account constraints of identity candidate c into av.
local function account_avail(c, av)
  local a = redis.call('HMGET', base .. 'acc:' .. c.acc, 'st', 'bu', 'cd')
  c.acd = sp_num(a[3], 0)
  if c.acd > av then
    av = c.acd
  end
  if a[1] == 'banned' then
    local bu = sp_num(a[2], 0)
    local t = bu
    if bu <= 0 then
      t = now + LONG_PUSH
    elseif bu <= now then
      t = now + 10000
    end
    if t > av then
      av = t
    end
  elseif a[1] == 'disabled' then
    if now + LONG_PUSH > av then
      av = now + LONG_PUSH
    end
  end
  return av
end

-- quota_push returns the time at which every quota window of c admits one
-- more request, or nil when the request fits now.
local function quota_push(c)
  local vals = redis.call('HMGET', base .. 'q:' .. eg .. ':' .. c.i, unpack(C.qfields))
  c.q = vals
  local factor = 1
  if C.warmup > 0 and c.act > 0 and now - c.act < C.warmup then
    factor = C.warmup_factor
  end
  local push = nil
  for q = 1, C.nq do
    local w = C.qwin[q]
    local idx = math.floor(now / w)
    local cidx = sp_num(vals[3 * q - 2], -1)
    local n = sp_num(vals[3 * q - 1], 0)
    local p = sp_num(vals[3 * q], 0)
    if cidx ~= idx then
      if cidx == idx - 1 then
        p = n
      else
        p = 0
      end
      n = 0
    end
    local start = idx * w
    local limit = C.qlim[q] * factor
    if limit < 1 then
      limit = 1
    end
    if sp_quota_est(p, n, start, w, now) + 1 > limit then
      local t = start + w
      if n + 1 <= limit and p > 0 then
        local tt = start + math.ceil(w * (1 - (limit - n - 1) / p)) + 1
        if tt < t then
          t = tt
        end
      end
      if t <= now then
        t = now + 1
      end
      if push == nil or t > push then
        push = t
      end
    end
  end
  return push
end

-- bind_check applies the bind_identity proxy constraints. It returns a push
-- time when the identity must wait, otherwise records on c either the usable
-- bound proxy (c.bound) or that a new binding is needed (c.need_bind).
local function bind_check(c)
  if c.px == '' then
    c.need_bind = true
    return nil
  end
  local ph = redis.call('HMGET', base .. 'px:' .. c.px, 'st', 'cd', 'gcd', 'al', 'mc', 'pid')
  if ph[1] == 'active' then
    local pav = math.max(sp_num(ph[2], 0), sp_num(ph[3], 0))
    if pav <= now then
      local pmc = sp_num(ph[5], 1)
      if sp_num(ph[4], 0) >= pmc then
        return short_push
      end
      c.bound = {p = c.px, pid = ph[6] or '', mc = pmc}
      return nil
    end
    if pav - now <= C.tolerance then
      return pav
    end
  end
  local used = 0
  if c.rbd == C.today then
    used = c.rbn
  end
  if used >= C.max_rebinds then
    return now + LONG_PUSH
  end
  c.need_bind = true
  c.old_px = c.px
  return nil
end

-- evaluate applies every candidate filter (spec §6.1 step 3) to identity i.
-- It returns "ok", candidate | "rem" | "push", score. Sticky evaluation
-- bypasses the reuse interval and never changes scores.
local function evaluate(i, hsraw, sticky)
  local v = redis.call('HMGET', base .. 'id:' .. i, 'st', 'al', 'xl', 'scd', 'sru', 'acc', 'act', 'px', 'rbd', 'rbn', 'rg',
    'iid', 'ty', 'pv', 'tv')
  local st = v[1]
  if st ~= 'active' and st ~= 'pending' then
    return 'rem'
  end
  local hs = sp_hs_unpack(hsraw, C.baseline, now)
  local c = {
    i = i, n = tonumber(i), st = st, hs = hs, al = sp_num(v[2], 0), xl = sp_num(v[3], 0),
    scd = sp_num(v[4], 0), sru = sp_num(v[5], 0), acc = v[6] or '', act = sp_num(v[7], 0),
    px = v[8] or '', rbd = v[9] or '', rbn = sp_num(v[10], 0), rg = v[11] or '', acd = 0,
    iid = v[12] or '', ty = v[13] or '', pv = v[14] or '0', tv = v[15] or '0',
  }
  local av = hs.cd
  if c.scd > av then av = c.scd end
  if not sticky then
    if hs.ru > av then av = hs.ru end
    if c.sru > av then av = c.sru end
  end
  -- An identity held by an exclusive lease (mc = 1) is pushed to that lease's
  -- expiry. Record it on the identity ("xg", written by the caller) so that
  -- ending the lease only has to walk the other groups when some group really
  -- pushed the identity; without the marker every lease end would be
  -- O(endpoint groups) (see lease_end.lua).
  push_xl = c.al > 0 and c.xl > now
  if c.al > 0 and c.xl > av then av = c.xl end
  if c.acc ~= '' then
    av = account_avail(c, av)
  end
  if av > now then
    return 'push', av
  end
  push_xl = false
  c.lim = C.mc
  if st == 'pending' and C.probe_max < c.lim then
    c.lim = C.probe_max
  end
  if c.al >= c.lim then
    return 'push', short_push
  end
  if C.nq > 0 then
    local qp = quota_push(c)
    if qp then
      return 'push', qp
    end
  end
  if C.pmode == 'b' then
    local bp = bind_check(c)
    if bp then
      return 'push', bp
    end
  end
  return 'ok', c
end
