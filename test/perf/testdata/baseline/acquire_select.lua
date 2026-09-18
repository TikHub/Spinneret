-- acquire_select.lua: candidate pools and selection strategies of acquire.lua
-- (spec §6.1 step 4).

-- hs_score returns the decayed health score of a packed hs entry, reading
-- only "score|sts" (plain finds are far cheaper than sp_hs_unpack in Lua).
local function hs_score(raw)
  if not raw then
    return C.baseline
  end
  local p1 = string.find(raw, '|', 1, true)
  if not p1 then
    return C.baseline
  end
  local sc = tonumber(string.sub(raw, 1, p1 - 1)) or C.baseline
  if C.tau > 0 then
    local p2 = string.find(raw, '|', p1 + 1, true)
    local sts = tonumber(string.sub(raw, p1 + 1, (p2 or 0) - 1))
    if sts and now > sts then
      sc = C.baseline + (sc - C.baseline) * math.exp((sts - now) / C.tau)
    end
  end
  return sc
end

-- hs_last_used returns the "lu" field (the last one) of a packed hs entry.
local function hs_last_used(raw)
  if not raw then
    return 0
  end
  local pos = 0
  for _ = 1, 7 do
    pos = string.find(raw, '|', pos + 1, true)
    if not pos then
      return 0
    end
  end
  return tonumber(string.sub(raw, pos + 1)) or 0
end

-- Candidate pool of one sampling round: parallel arrays (no per-candidate
-- tables) of member ids, packed hs entries, lazily computed selection keys
-- (weights or sort keys) and verified candidates.
local function new_pool(ids, raws, limit)
  local P = {ids = {}, raws = {}, key = {}, c = {}, n = 0, exact = false}
  for j, i in ipairs(ids) do
    if P.n >= limit then
      break
    end
    if not skip[i] then
      P.n = P.n + 1
      P.ids[P.n] = i
      P.raws[P.n] = raws[j]
      P.key[P.n] = false
      P.c[P.n] = false
    end
  end
  return P
end

-- pool_remove drops entry j (swap with the last entry).
local function pool_remove(P, j)
  local last = P.n
  P.ids[j], P.raws[j], P.key[j], P.c[j] = P.ids[last], P.raws[last], P.key[last], P.c[last]
  P.ids[last], P.raws[last], P.key[last], P.c[last] = nil, nil, nil, nil
  P.n = last - 1
end

local WEIGHT_MAX = 10000

-- weight_of returns max(min(score, 100), 5)^2 of a packed hs entry.
local function weight_of(raw)
  local sc = hs_score(raw)
  if sc < 5 then
    sc = 5
  elseif sc > 100 then
    sc = 100
  end
  return sc * sc
end

-- pick_index chooses the next pool entry per strategy (spec §6.1 step 4).
-- Filters are applied lazily to the chosen entry by the caller: removing
-- filtered entries and choosing again yields exactly the distribution of
-- selecting among survivors, while only chosen entries pay for the full
-- evaluation. weighted_random is exact rejection sampling: a few uniform
-- proposals accepted with probability weight / 100^2 (parsing only proposed
-- entries), then a roulette over every weight; weights of verified pending
-- identities already include probe.weight_factor.
local function pick_index(P)
  local s = C.strategy
  local n = P.n
  if s == 'w' then
    if not P.exact then
      for _ = 1, 8 do
        local j = math.floor(rnd() * n) + 1
        if j > n then
          j = n
        end
        local w = P.key[j]
        if not w then
          w = weight_of(P.raws[j])
          P.key[j] = w
        end
        if rnd() * WEIGHT_MAX < w then
          return j
        end
      end
      P.exact = true
    end
    local total = 0
    for j = 1, n do
      local w = P.key[j]
      if not w then
        w = weight_of(P.raws[j])
        P.key[j] = w
      end
      total = total + w
    end
    local target = rnd() * total
    local acc = 0
    for j = 1, n do
      acc = acc + P.key[j]
      if target < acc then
        return j
      end
    end
    return n
  end
  if s == 'r' then
    if rr_cursor == nil then
      rr_cursor = sp_num(redis.call('GET', base .. 'rr:' .. eg), 0)
    end
    local after, lowest = nil, 1
    for j = 1, n do
      local k = P.key[j]
      if not k then
        k = tonumber(P.ids[j])
        P.key[j] = k
      end
      if k > rr_cursor and (after == nil or k < P.key[after]) then
        after = j
      end
      if k < (P.key[lowest] or tonumber(P.ids[lowest])) then
        lowest = j
      end
    end
    return after or lowest
  end
  local idx = 1
  for j = 1, n do
    local k = P.key[j]
    if not k then
      if s == 'l' then
        k = hs_last_used(P.raws[j])
      else
        k = hs_score(P.raws[j])
      end
      P.key[j] = k
    end
    if j > 1 and ((s == 'l' and k < P.key[idx]) or (s ~= 'l' and k > P.key[idx])) then
      idx = j
    end
  end
  return idx
end
