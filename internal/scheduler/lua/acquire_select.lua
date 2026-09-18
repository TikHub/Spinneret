-- acquire_select.lua: candidate pools and selection strategies of acquire.lua
-- (spec §6.1 step 4).

-- hs_score returns the decayed health score of a packed hs entry, reading
-- only "score|sts" (plain finds are far cheaper than sp_hs_unpack in Lua).
-- sp_dnum is tonumber without the slow path Lua takes for a "%.2f" score and
-- for a 13-digit timestamp.
local function hs_score(raw)
  if not raw then
    return baseline
  end
  local p1 = string.find(raw, '|', 1, true)
  if not p1 then
    return baseline
  end
  local sc = sp_dnum(string.sub(raw, 1, p1 - 1)) or baseline
  if tau > 0 then
    local p2 = string.find(raw, '|', p1 + 1, true)
    local sts = sp_dnum(string.sub(raw, p1 + 1, (p2 or 0) - 1))
    if sts and now > sts then
      sc = baseline + (sc - baseline) * math.exp((sts - now) / tau)
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
  return sp_dnum(string.sub(raw, pos + 1)) or 0
end

-- Candidate pool of one sampling round: parallel arrays (no per-candidate
-- tables) of member ids, packed hs entries, lazily computed selection keys
-- (weights or sort keys) and verified candidates. raws, key and c stay empty
-- until an entry is looked at; every reader tests key and c with "not", for
-- which an absent entry and a false entry are the same.
--
-- taken is the number of identities this call has already leased. While it is
-- zero nothing can be filtered out and the ZRANGEBYSCORE limit was the pool
-- limit, so the reply array is the pool and is aliased rather than copied
-- (copying it costs 0.9 us at candidate_sample 32). The caller must therefore
-- read #ids before the pool is used, because pool_remove shortens the aliased
-- array.
local function new_pool(ids, limit, taken)
  local n = #ids
  if taken == 0 and n <= limit then
    return {ids = ids, raws = {}, key = {}, c = {}, n = n, exact = false}
  end
  local P = {ids = {}, raws = {}, key = {}, c = {}, n = 0, exact = false}
  for j = 1, n do
    if P.n >= limit then
      break
    end
    local i = ids[j]
    if not skip[i] then
      P.n = P.n + 1
      P.ids[P.n] = i
    end
  end
  return P
end

-- pool_raw returns the packed hs entry of pool entry j, reading it on demand.
-- weighted_random, the default strategy, looks at two or three of the sampled
-- candidates before one is accepted, so reading the whole sample up front (one
-- HMGET over candidate_sample fields, and as many interned Lua strings) costs
-- more than the individual HGETs of the entries that are really examined:
-- measured at candidate_sample 32, HMGET of 32 fields is 3.1 us against
-- 0.39 us for one HGET. Strategies that rank the whole sample call pool_load
-- first, which reads every entry still missing in one HMGET.
local function pool_raw(P, j)
  local r = P.raws[j]
  if r == nil then
    r = redis.call('HGET', hskey, P.ids[j])
    P.raws[j] = r
  end
  return r
end

-- pool_load reads the packed hs entries of every pool entry that has not been
-- read yet, in one HMGET.
local function pool_load(P)
  local miss, at, m = nil, nil, 0
  for j = 1, P.n do
    if P.raws[j] == nil then
      m = m + 1
      if m == 1 then
        miss, at = {}, {}
      end
      miss[m] = P.ids[j]
      at[m] = j
    end
  end
  if m == 0 then
    return
  end
  local vals = redis.call('HMGET', hskey, unpack(miss))
  for k = 1, m do
    P.raws[at[k]] = vals[k]
  end
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
  local s = strategy
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
          w = weight_of(pool_raw(P, j))
          P.key[j] = w
        end
        if rnd() * WEIGHT_MAX < w then
          return j
        end
      end
      P.exact = true
    end
    local total = 0
    pool_load(P)
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
  pool_load(P)
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
