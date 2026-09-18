--[[
Spinneret common Lua prelude (internal/store/redis/lua/common.lua).

This file is prepended to every script registered with store/redis.NewScript,
so every helper below is callable from any script body. Redis and Valkey forbid
scripts from creating global variables, therefore the helpers are chunk-level
locals: they are visible to the script body that follows, but a body must not
redeclare their names.

Conventions (spec §5):
  * KEYS[1] of every site script is "P:T:meta"; the site base "P:T:" is derived
    from it with sp_base().
  * Numeric hash fields are base-10 strings; timestamps are Unix milliseconds.
  * Redis embeds Lua 5.1 where every number is a double. Integral values are
    exact up to 2^53 and are formatted with "%.0f": "%d" casts through a C
    long (32 bits on some platforms, which would break millisecond timestamps
    such as 1758011411962) and plain concatenation uses "%.14g", which switches
    to exponent notation above 14 digits.
  * Key parts (endpoint group, identity, account and proxy hkeys) may be passed
    as strings or integral numbers.
]]

--[[
sp_int_str(x) -> string
Formats x as a base-10 integer string (floor, no exponent, "-0" normalized to
"0"). nil, false, "" and non-numeric values format as "0".
]]
local function sp_int_str(x)
  local n = tonumber(x)
  if n == nil or n ~= n or n == math.huge or n == -math.huge then
    return '0'
  end
  local s = string.format('%.0f', math.floor(n))
  if s == '-0' then
    return '0'
  end
  return s
end

--[[
sp_str(v) -> string
Returns v unchanged when it is a string, its integer representation when it is
a number (avoids Lua's "%.14g" coercion in concatenation), and "" otherwise.
]]
local function sp_str(v)
  local t = type(v)
  if t == 'string' then
    return v
  elseif t == 'number' then
    return sp_int_str(v)
  end
  return ''
end

--[[
sp_score_str(x) -> string
Formats a ZSET score losslessly ("%.17g"); integral scores keep their plain
integer form, e.g. 1758011411962.
]]
local function sp_score_str(x)
  local n = tonumber(x) or 0
  if n == math.floor(n) and n > -9007199254740992 and n < 9007199254740992 then
    return sp_int_str(n)
  end
  return string.format('%.17g', n)
end

--[[
sp_base() -> "P:T:"
Derives the site key base from KEYS[1] ("P:T:meta") by stripping the trailing
"meta". Raises a script error when KEYS[1] is missing or is not a meta key.
]]
local function sp_base()
  local k = KEYS[1]
  if type(k) ~= 'string' or string.len(k) < 5 or string.sub(k, -4) ~= 'meta' then
    error('sp_base: KEYS[1] must be the site meta key "P:T:meta"')
  end
  return string.sub(k, 1, -5)
end

--[[
sp_num(v, default) -> number
tonumber with a default: nil, false (a missing Redis value), "", non-numeric
strings, NaN and infinities yield default.
]]
local function sp_num(v, default)
  if v == nil or v == false or v == '' then
    return default
  end
  local n = tonumber(v)
  if n == nil or n ~= n or n == math.huge or n == -math.huge then
    return default
  end
  return n
end

--[[
sp_split(s, sep) -> array of strings
Splits s on the literal separator sep (no pattern matching). Empty fields are
preserved ("a||b" -> {"a", "", "b"}). nil, false and "" yield an empty array;
an empty separator yields {s}.
]]
local function sp_split(s, sep)
  local out = {}
  if s == nil or s == false or s == '' then
    return out
  end
  s = tostring(s)
  if sep == nil or sep == '' then
    out[1] = s
    return out
  end
  local pos, n = 1, 0
  while true do
    local i, j = string.find(s, sep, pos, true)
    n = n + 1
    if not i then
      out[n] = string.sub(s, pos)
      return out
    end
    out[n] = string.sub(s, pos, i - 1)
    pos = j + 1
  end
end

--[[
sp_hs_unpack(s, baseline, now) -> table
Decodes the packed identity x endpoint-group state "score|sts|samples|nfail|
lastfail|cd|ru|lu" (spec §5.1) into
  {score=, sts=, samples=, nfail=, lastfail=, cd=, ru=, lu=}
A missing entry (nil/false/"") yields score=baseline, sts=now and zeros for the
other fields; missing or malformed individual fields get the same defaults.
nil baseline/now are treated as 0.
]]
local function sp_hs_unpack(s, baseline, now)
  local p = sp_split(s, '|')
  baseline = sp_num(baseline, 0)
  now = sp_num(now, 0)
  return {
    score = sp_num(p[1], baseline),
    sts = sp_num(p[2], now),
    samples = sp_num(p[3], 0),
    nfail = sp_num(p[4], 0),
    lastfail = sp_num(p[5], 0),
    cd = sp_num(p[6], 0),
    ru = sp_num(p[7], 0),
    lu = sp_num(p[8], 0),
  }
end

--[[
sp_hs_pack(t) -> string
Encodes a table produced by sp_hs_unpack as "score|sts|samples|nfail|lastfail|
cd|ru|lu": score with two decimals ("%.2f"), every other field as an integer
string (floor). Missing fields encode as 0.
]]
local function sp_hs_pack(t)
  local score = string.format('%.2f', sp_num(t.score, 0))
  if score == '-0.00' then
    score = '0.00'
  end
  return score
    .. '|' .. sp_int_str(t.sts)
    .. '|' .. sp_int_str(t.samples)
    .. '|' .. sp_int_str(t.nfail)
    .. '|' .. sp_int_str(t.lastfail)
    .. '|' .. sp_int_str(t.cd)
    .. '|' .. sp_int_str(t.ru)
    .. '|' .. sp_int_str(t.lu)
end

--[[
sp_hs_get(base, eg, i, baseline, now) -> table
Reads HGET base.."hs:"..eg i and decodes it with sp_hs_unpack.
]]
local function sp_hs_get(base, eg, i, baseline, now)
  local v = redis.call('HGET', base .. 'hs:' .. sp_str(eg), sp_str(i))
  return sp_hs_unpack(v, baseline, now)
end

--[[
sp_hs_set(base, eg, i, t) -> string
Writes HSET base.."hs:"..eg i sp_hs_pack(t) and returns the packed value.
]]
local function sp_hs_set(base, eg, i, t)
  local packed = sp_hs_pack(t)
  redis.call('HSET', base .. 'hs:' .. sp_str(eg), sp_str(i), packed)
  return packed
end

--[[
sp_decay(score, sts, now, baseline, tau_ms) -> number
Exponential regression of score towards baseline for the time elapsed since
sts: baseline + (score - baseline) * exp(-(now - sts) / tau_ms) (spec §6.4).
No decay is applied when tau_ms <= 0 or now <= sts.
]]
local function sp_decay(score, sts, now, baseline, tau_ms)
  score = sp_num(score, 0)
  local dt = sp_num(now, 0) - sp_num(sts, 0)
  tau_ms = sp_num(tau_ms, 0)
  if tau_ms <= 0 or dt <= 0 then
    return score
  end
  baseline = sp_num(baseline, 0)
  return baseline + (score - baseline) * math.exp(-dt / tau_ms)
end

--[[
sp_ewma(score, v, alpha) -> number
Exponentially weighted moving average update alpha*v + (1-alpha)*score, with
alpha clamped to [0, 1].
]]
local function sp_ewma(score, v, alpha)
  alpha = sp_num(alpha, 0)
  if alpha < 0 then
    alpha = 0
  elseif alpha > 1 then
    alpha = 1
  end
  return alpha * sp_num(v, 0) + (1 - alpha) * sp_num(score, 0)
end

--[[
sp_avail(base, eg, i) -> number (ms)
Availability rule (spec §5.6):
  max(hs.cd, hs.ru, id.scd, id.sru, (id.al > 0 and id.xl or 0),
      acc.cd when id.acc ~= "")
Missing hashes and fields count as 0.
]]
local function sp_avail(base, eg, i)
  i = sp_str(i)
  local hs = sp_hs_unpack(redis.call('HGET', base .. 'hs:' .. sp_str(eg), i), 0, 0)
  local id = redis.call('HMGET', base .. 'id:' .. i, 'scd', 'sru', 'al', 'xl', 'acc')
  local m = hs.cd
  if hs.ru > m then m = hs.ru end
  local scd = sp_num(id[1], 0)
  if scd > m then m = scd end
  local sru = sp_num(id[2], 0)
  if sru > m then m = sru end
  if sp_num(id[3], 0) > 0 then
    local xl = sp_num(id[4], 0)
    if xl > m then m = xl end
  end
  local acc = id[5]
  if acc and acc ~= '' then
    local acd = sp_num(redis.call('HGET', base .. 'acc:' .. acc, 'cd'), 0)
    if acd > m then m = acd end
  end
  return m
end

--[[
sp_push_all(base, egs, i, score) -> integer
Pushes identity i to score on every "rdy:<eg>" of egs (array of endpoint group
hkeys) with ZADD XX GT: members that are absent are not added and scores never
move backwards. Returns the number of ZSETs whose score changed.
]]
local function sp_push_all(base, egs, i, score)
  local changed = 0
  if type(egs) ~= 'table' then
    return changed
  end
  i = sp_str(i)
  local s = sp_score_str(score)
  for _, eg in ipairs(egs) do
    changed = changed + redis.call('ZADD', base .. 'rdy:' .. sp_str(eg), 'XX', 'GT', 'CH', s, i)
  end
  return changed
end

--[[
sp_rescore(base, eg, i) -> number (ms)
Sets the score of identity i in "rdy:<eg>" to sp_avail(base, eg, i) with
ZADD XX (only when the member is present; the score may move in either
direction). Returns the computed availability.
]]
local function sp_rescore(base, eg, i)
  local s = sp_avail(base, eg, i)
  redis.call('ZADD', base .. 'rdy:' .. sp_str(eg), 'XX', sp_score_str(s), sp_str(i))
  return s
end

--[[
sp_quota_est(p, n, window_idx_start_ms, w, now) -> number
Sliding-window quota estimate: p * (1 - elapsed / w) + n, where p is the
previous window count, n the current window count and elapsed = now -
window_idx_start_ms. elapsed/w is clamped to [0, 1], so the estimate is never
below n nor above p + n. w <= 0 yields n.
]]
local function sp_quota_est(p, n, window_idx_start_ms, w, now)
  p = sp_num(p, 0)
  n = sp_num(n, 0)
  w = sp_num(w, 0)
  if w <= 0 then
    return n
  end
  local frac = (sp_num(now, 0) - sp_num(window_idx_start_ms, 0)) / w
  if frac < 0 then
    frac = 0
  elseif frac > 1 then
    frac = 1
  end
  local est = p * (1 - frac) + n
  if est < n then
    est = n
  end
  return est
end

--[[
sp_hmget_map(key, fields) -> table
HMGET key fields... returned as a table field -> value; fields that are missing
are absent from the table (index yields nil). An empty field list performs no
Redis call and returns an empty table.
]]
local function sp_hmget_map(key, fields)
  local out = {}
  if type(fields) ~= 'table' or #fields == 0 then
    return out
  end
  local vals = redis.call('HMGET', key, unpack(fields))
  for idx, f in ipairs(fields) do
    local v = vals[idx]
    if v ~= nil and v ~= false then
      out[f] = v
    end
  end
  return out
end
