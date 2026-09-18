-- acquire_main.lua: control flow of acquire.lua (spec §6.1 steps 1-7).

-- Step 1: breaker gate.
local b = redis.call('HMGET', brkkey, 'st', 'ou', 'man', 'hw', 'hc')
local bst = b[1]
if bst == 'open' then
  local ou = sp_num(b[2], 0)
  if b[3] == '1' and ou == 0 then
    return {'BREAKER_OPEN', transition, '60000'}
  end
  if now < ou then
    return {'BREAKER_OPEN', transition, sp_int_str(ou - now)}
  end
  redis.call('HSET', brkkey, 'st', 'half_open', 'hw', sp_int_str(now), 'hc', '0', 'ps', '0', 'pk', '0')
  redis.call('HINCRBY', brkkey, 'v', 1)
  transition = '1'
  bst = 'half_open'
  b[4] = now
  b[5] = 0
end
if bst == 'half_open' then
  local hw = sp_num(b[4], 0)
  local hc = sp_num(b[5], 0)
  if now - hw >= PROBE_WINDOW then
    hw = now
    hc = 0
    redis.call('HSET', brkkey, 'hw', sp_int_str(now), 'hc', '0')
  end
  if hc >= C.probe_limit then
    return {'BREAKER_OPEN', transition, sp_int_str(math.max(hw + PROBE_WINDOW - now, 1))}
  end
  probe = true
  C.count = 1
  C.strategy = 'b'
end

-- Half-open probes bypass stickiness: they must go to the healthiest
-- identities (a sticky low-health identity would blame the endpoint for its
-- own problems) and must not rebind the session.
if C.session ~= '' and C.sticky_ttl > 0 and C.count == 1 and not probe then
  stkkey = base .. 'stk:' .. eg .. ':' .. C.session
end

-- Step 2: sticky reuse.
local proxy_fail = 0
if stkkey then
  local si = redis.call('GET', stkkey)
  if si and redis.call('ZSCORE', rdy, si) then
    local kind, c = evaluate(si, redis.call('HGET', hskey, si), true)
    if kind == 'ok' then
      local ok, proxy = assign_proxy(c)
      if ok then
        picked = 1
        write_lease(c, C.prefixes[1], proxy, true)
      else
        proxy_fail = 1
        skip[si] = true
        nskip = 1
      end
    end
  end
end

-- Steps 3-6: sample, filter, select, assign proxies, write leases.
if picked < C.count then
  for _ = 1, 3 do
    -- A round samples K candidates, or as many as the batch still needs.
    -- lim is fixed before the round: its picks grow nskip, and the due range
    -- is exhausted only when fewer than lim members were returned.
    local want = math.max(C.k, C.count - picked)
    local lim = want + nskip
    local ids = redis.call('ZRANGEBYSCORE', rdy, '-inf', now, 'LIMIT', 0, lim)
    if #ids == 0 then
      break
    end
    local P = new_pool(ids, redis.call('HMGET', hskey, unpack(ids)), want)
    while picked < C.count and P.n > 0 and proxy_fail < 3 do
      local j = pick_index(P)
      local c = P.c[j]
      local take = true
      if not c then
        local kind, v = evaluate(P.ids[j], P.raws[j], false)
        if kind == 'ok' then
          c = v
          P.c[j] = v
          if C.strategy == 'w' and v.st == 'pending' and C.probe_wf < 1 then
            P.key[j] = (P.key[j] or weight_of(P.raws[j])) * C.probe_wf
            take = rnd() < C.probe_wf
          end
        else
          if kind == 'rem' then
            redis.call('ZREM', rdy, P.ids[j])
          else
            redis.call('ZADD', rdy, 'XX', sp_score_str(v), P.ids[j])
            if push_xl then
              -- Mark the identity so the exclusive lease restores this score
              -- when it ends instead of scanning every group unconditionally.
              redis.call('HSET', base .. 'id:' .. P.ids[j], 'xg', '1')
            end
          end
          pool_remove(P, j)
          take = false
        end
      end
      if take then
        pool_remove(P, j)
        skip[c.i] = true
        nskip = nskip + 1
        local ok, proxy = assign_proxy(c)
        if ok then
          picked = picked + 1
          if C.strategy == 'r' then
            rr_cursor = c.n
          end
          write_lease(c, C.prefixes[picked], proxy, false)
        else
          proxy_fail = proxy_fail + 1
        end
      end
    end
    if picked >= C.count or proxy_fail >= 3 or #ids < lim then
      break
    end
  end
end

if picked > 0 then
  if rr_cursor ~= nil then
    redis.call('SET', base .. 'rr:' .. eg, sp_int_str(rr_cursor))
  end
  redis.call('ZADD', base .. 'aeg', sp_int_str(now), eg)
  out[2] = transition
  out[3] = sp_int_str(picked)
  return out
end

-- retry_after clamps the wait until the first member of zset becomes due.
local function retry_after(zset, fallback)
  local first = redis.call('ZRANGE', zset, 0, 0, 'WITHSCORES')
  if #first < 2 then
    return 60000
  end
  local d = tonumber(first[2]) - now
  if d <= 0 then
    return fallback
  end
  return math.min(math.max(d, 50), 60000)
end

if proxy_fail > 0 then
  return {'NO_PROXY', transition, sp_int_str(retry_after(pxrdy, 1000))}
end
return {'EXHAUSTED', transition, sp_int_str(retry_after(rdy, 50))}
