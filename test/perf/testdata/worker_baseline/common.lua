--[[
Spinneret common Lua prelude (internal/store/redis/lua/common.lua).

store/redis.NewScript prepends the helpers a script body actually uses to that
body, so every helper below is callable from any script body that names it.
Redis and Valkey forbid scripts from creating global variables, therefore the
helpers are chunk-level locals: they are visible to the script body that
follows, but a body must not redeclare their names.

Each helper starts with a "--@sp <name>" marker line. NewScript splits this
file on those markers, resolves the transitive dependencies of the sp_* names
that appear in the body, and emits only those sections (in file order, so a
helper is always defined before the helpers that call it). Everything above the
first marker is the shared header and is always emitted. Creating a chunk-level
closure costs about 0.14 us per call on Valkey 8.1, so a script that uses two
helpers must not pay for eighteen.

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
  * An *argument of redis.call* that is known to be an integral number of
    magnitude at most 2^53 may be passed as a number instead of through
    sp_int_str: the server renders it with the shortest representation that
    round-trips, which for such a value is exactly its digits (verified on
    Valkey 8.1: 1758011411962, 9007199254740992, and -0 as "0"). That skips a
    string.format and an interned Lua string per field, which the lease write
    of acquire pays a dozen times. It applies to redis.call arguments only:
    a number placed in a *reply* table is returned as a RESP integer, and a
    number concatenated into a string still goes through "%.14g".
]]

--@sp sp_int_str
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
  n = math.floor(n)
  -- Comparing against 0 also catches -0, which "%.0f" would render as "-0",
  -- and skips the C formatting call for the most frequent value by far.
  if n == 0 then
    return '0'
  end
  return string.format('%.0f', n)
end

--@sp sp_str
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

--@sp sp_score_str
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

--@sp sp_base
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

--@sp sp_dnum
--[[
sp_dnum(s) -> number | nil
tonumber(s) without the slow path Lua 5.1 takes for long numerals. Measured on
Valkey 8.1.10: tonumber costs 0.07 us for up to nine digits and 0.58 us from ten
digits on, because the C library stops accumulating in integer arithmetic there
- and the hot state is full of 13-digit millisecond timestamps.

A run of ten to fifteen digits is parsed as two halves of at most nine digits
each and recombined. That is exact and not an approximation: each half, the
product and the sum are integers far below 2^53, where every integer is a
double. Verified against tonumber on 300 000 random numerals of 10 to 15
digits, all equal.

Every other shape - shorter, longer, signed, fractional, exponent, whitespace,
non-numeric - is handed to tonumber unchanged, so the result is always
tonumber's result. A fraction is deliberately not fast-pathed: splitting
"<int>.<frac>" into two integers rounds twice and disagrees with tonumber in
the last bit for about one value in 500 (1.14 is the first).
]]
local function sp_dnum(s)
  local n = #s
  if n > 9 and n < 16 and string.find(s, '^%d+$') then
    return tonumber(string.sub(s, 1, n - 9)) * 1000000000 + tonumber(string.sub(s, n - 8))
  end
  return tonumber(s)
end

--@sp sp_num
--[[
sp_num(v, default) -> number
tonumber with a default: nil, false (a missing Redis value), "", non-numeric
strings, NaN and infinities yield default.
]]
local function sp_num(v, default)
  if v == nil or v == false or v == '' then
    return default
  end
  local n
  if type(v) == 'string' then
    n = sp_dnum(v)
  else
    n = tonumber(v)
  end
  if n == nil or n ~= n or n == math.huge or n == -math.huge then
    return default
  end
  return n
end

--@sp sp_split
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

--@sp sp_hs_unpack
--[[
sp_hs_unpack(s, baseline, now) -> table
Decodes the packed identity x endpoint-group state "score|sts|samples|nfail|
lastfail|cd|ru|lu" (spec §5.1) into
  {score=, sts=, samples=, nfail=, lastfail=, cd=, ru=, lu=}
A missing entry (nil/false/"") yields score=baseline, sts=now and zeros for the
other fields; missing or malformed individual fields get the same defaults.
nil baseline/now are treated as 0.

The eight fields are cut with a single string.match (0.24 us) instead of a
split into a throwaway table (0.79 us); a value that does not have exactly
eight fields falls back to sp_split, which also covers the short forms written
by older releases.
]]
local function sp_hs_unpack(s, baseline, now)
  baseline = sp_num(baseline, 0)
  now = sp_num(now, 0)
  if s == nil or s == false or s == '' then
    return {score = baseline, sts = now, samples = 0, nfail = 0, lastfail = 0, cd = 0, ru = 0, lu = 0}
  end
  local f1, f2, f3, f4, f5, f6, f7, f8 = string.match(s,
    '^([^|]*)|([^|]*)|([^|]*)|([^|]*)|([^|]*)|([^|]*)|([^|]*)|([^|]*)$')
  if f1 == nil then
    local p = sp_split(s, '|')
    f1, f2, f3, f4, f5, f6, f7, f8 = p[1], p[2], p[3], p[4], p[5], p[6], p[7], p[8]
  end
  return {
    score = sp_num(f1, baseline),
    sts = sp_num(f2, now),
    samples = sp_num(f3, 0),
    nfail = sp_num(f4, 0),
    lastfail = sp_num(f5, 0),
    cd = sp_num(f6, 0),
    ru = sp_num(f7, 0),
    lu = sp_num(f8, 0),
  }
end

--@sp sp_hs_pack
--[[
sp_hs_pack(t) -> string
Encodes a table produced by sp_hs_unpack as "score|sts|samples|nfail|lastfail|
cd|ru|lu": score with two decimals ("%.2f"), every other field as an integer
string (floor). Missing fields encode as 0.

The seven integers share one string.format call; -0 is normalized on the value
(a float compare) rather than on the formatted text, and the score keeps its
own call so that the "-0.00" of a small negative score is still normalized.
]]
local function sp_hs_pack(t)
  local score = string.format('%.2f', sp_num(t.score, 0))
  if score == '-0.00' then
    score = '0.00'
  end
  local sts = math.floor(sp_num(t.sts, 0))
  local samples = math.floor(sp_num(t.samples, 0))
  local nfail = math.floor(sp_num(t.nfail, 0))
  local lastfail = math.floor(sp_num(t.lastfail, 0))
  local cd = math.floor(sp_num(t.cd, 0))
  local ru = math.floor(sp_num(t.ru, 0))
  local lu = math.floor(sp_num(t.lu, 0))
  -- "%.0f" renders -0 as "-0"; assigning 0 to a value that compares equal to 0
  -- replaces a negative zero with a positive one.
  if sts == 0 then sts = 0 end
  if samples == 0 then samples = 0 end
  if nfail == 0 then nfail = 0 end
  if lastfail == 0 then lastfail = 0 end
  if cd == 0 then cd = 0 end
  if ru == 0 then ru = 0 end
  if lu == 0 then lu = 0 end
  return score .. string.format('|%.0f|%.0f|%.0f|%.0f|%.0f|%.0f|%.0f',
    sts, samples, nfail, lastfail, cd, ru, lu)
end

--@sp sp_hs_cd_ru
--[[
sp_hs_cd_ru(s) -> cd, ru (both ms)
Reads the "cd" and "ru" fields of a packed identity x endpoint-group state
without decoding the other six or building a table. A caller that only has to
know when the identity is available again (spec §5.6) pays 0.6 us instead of
the 2.3 us of sp_hs_unpack. Missing, empty and malformed entries yield 0, 0,
exactly as sp_hs_unpack does for those two fields with a zero baseline.
]]
local function sp_hs_cd_ru(s)
  if s == nil or s == false or s == '' then
    return 0, 0
  end
  local cd, ru = string.match(s, '^[^|]*|[^|]*|[^|]*|[^|]*|[^|]*|([^|]*)|([^|]*)|[^|]*$')
  if cd == nil then
    local t = sp_hs_unpack(s, 0, 0)
    return t.cd, t.ru
  end
  return sp_num(cd, 0), sp_num(ru, 0)
end

--@sp sp_hs_get
--[[
sp_hs_get(base, eg, i, baseline, now) -> table
Reads HGET base.."hs:"..eg i and decodes it with sp_hs_unpack.
]]
local function sp_hs_get(base, eg, i, baseline, now)
  local v = redis.call('HGET', base .. 'hs:' .. sp_str(eg), sp_str(i))
  return sp_hs_unpack(v, baseline, now)
end

--@sp sp_hs_set
--[[
sp_hs_set(base, eg, i, t) -> string
Writes HSET base.."hs:"..eg i sp_hs_pack(t) and returns the packed value.
]]
local function sp_hs_set(base, eg, i, t)
  local packed = sp_hs_pack(t)
  redis.call('HSET', base .. 'hs:' .. sp_str(eg), sp_str(i), packed)
  return packed
end

--@sp sp_decay
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

--@sp sp_ewma
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

--@sp sp_avail
--[[
sp_avail(base, eg, i) -> number (ms)
Availability rule (spec §5.6):
  max(hs.cd, hs.ru, id.scd, id.sru, (id.al > 0 and id.xl or 0),
      acc.cd when id.acc ~= "")
Missing hashes and fields count as 0.
]]
local function sp_avail(base, eg, i)
  i = sp_str(i)
  local cd, ru = sp_hs_cd_ru(redis.call('HGET', base .. 'hs:' .. sp_str(eg), i))
  local id = redis.call('HMGET', base .. 'id:' .. i, 'scd', 'sru', 'al', 'xl', 'acc')
  local m = cd
  if ru > m then m = ru end
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

--@sp sp_push_all
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

--@sp sp_rescore
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

--@sp sp_quota_est
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

--@sp sp_hmget_map
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
