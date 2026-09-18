-- acquire_proxy.lua: proxy selection of acquire.lua (spec §6.1 step 5).

-- select_proxy samples available proxies of the site and picks one weighted
-- by max(score, 5)^2 among those passing the policy filters. Proxies that
-- cannot serve any lease right now leave the due range of pxrdy: inactive or
-- missing proxies are removed (pxrdy only holds active proxies), cooling ones
-- are pushed to their cooldown end and full ones by min(ttl, 5 s), so they do
-- not crowd out usable proxies in later samples. Window offsets account for
-- the members that left the due range during this call.
local function select_proxy(region, exclude)
  local total = redis.call('ZCOUNT', pxrdy, '-inf', now)
  if total == 0 then
    return nil
  end
  local cands, tw, seen, gone = {}, 0, {}, 0
  local rounds = math.min(math.ceil(total / 16), 3)
  for round = 1, rounds do
    local due = total - gone
    if due <= 0 then
      break
    end
    local off = math.max((round - 1) * 16 - gone, 0)
    if total > 48 then
      off = math.floor(rnd() * math.max(due - 15, 1))
    end
    local ids = redis.call('ZRANGEBYSCORE', pxrdy, '-inf', now, 'LIMIT', off, 16)
    for _, p in ipairs(ids) do
      if p ~= exclude and not seen[p] then
        seen[p] = true
        local h = redis.call('HMGET', base .. 'px:' .. p, 'st', 'kd', 'rg', 'pv', 'tg', 'mc', 'al', 'sc', 'sts', 'cd', 'gcd', 'pid')
        if h[1] ~= 'active' then
          redis.call('ZREM', pxrdy, p)
          gone = gone + 1
        else
          local pav = math.max(sp_num(h[10], 0), sp_num(h[11], 0))
          local mcp = sp_num(h[6], 1)
          local ok = true
          if pav > now then
            redis.call('ZADD', pxrdy, 'XX', 'GT', sp_int_str(pav), p)
            gone = gone + 1
            ok = false
          elseif sp_num(h[7], 0) >= mcp then
            redis.call('ZADD', pxrdy, 'XX', 'GT', sp_int_str(short_push), p)
            gone = gone + 1
            ok = false
          end
          if ok and C.kinds and not C.kinds[h[2] or ''] then ok = false end
          if ok and C.providers and not C.providers[h[4] or ''] then ok = false end
          if ok and C.regions and not C.regions[h[3] or ''] then ok = false end
          if ok and region ~= nil and (h[3] or '') ~= region then ok = false end
          if ok then
            local tg = h[5] or ''
            for _, t in ipairs(C.tags) do
              if not string.find(tg, t, 1, true) then
                ok = false
                break
              end
            end
          end
          if ok then
            local s = sp_decay(sp_num(h[8], C.baseline), sp_num(h[9], now), now, C.baseline, C.tau)
            if s < 5 then s = 5 end
            cands[#cands + 1] = {p = p, pid = h[12] or '', mc = mcp, w = s * s}
            tw = tw + s * s
          end
        end
      end
    end
    if #cands > 0 then
      break
    end
  end
  if #cands == 0 then
    return nil
  end
  local target = rnd() * tw
  local acc = 0
  for _, c in ipairs(cands) do
    acc = acc + c.w
    if target < acc then
      return c
    end
  end
  return cands[#cands]
end

-- assign_proxy resolves the proxy of candidate c per proxy mode. It returns
-- false when no proxy is available, otherwise true and the proxy (nil for mode none).
local function assign_proxy(c)
  local mode = C.pmode
  if mode == 'n' then
    return true, nil
  end
  local region = nil
  if C.pregion or mode == 'r' then
    region = c.rg
  end
  if mode == 'b' then
    if c.bound then
      if picked > 0 then
        local al = sp_num(redis.call('HGET', base .. 'px:' .. c.bound.p, 'al'), 0)
        if al >= c.bound.mc then
          return false
        end
      end
      return true, c.bound
    end
    local sel = select_proxy(region, c.old_px)
    if not sel then
      return false
    end
    sel.bind = true
    sel.rebound = c.old_px ~= nil and c.old_px ~= ''
    return true, sel
  end
  local sel = select_proxy(region, nil)
  if not sel then
    return false
  end
  return true, sel
end
