--[[
proxy cooldown.lua — sets a manual proxy cooldown on one site (spec §5.4).

KEYS[1] = "P:T:meta"
ARGV[1] = proxy hkey
ARGV[2] = field: "cd" (proxy x site cooldown) or "gcd" (global cooldown)
ARGV[3] = cooldown until (Unix ms, in the future)
ARGV[4] = proxy id (kept in "pid" so a hash created here stays attributable)

The field is overwritten (a manual cooldown may shorten an automatic one).
When the proxy is in the pool ready queue "pxrdy" (active proxies only), its
score is moved to avail = max(cd, gcd): later with ZADD XX, or earlier when
the proxy is not saturated (al < mc; a saturated proxy keeps the later score
pushed by acquire until lease_end.lua pulls it back). Members are never added.
"p<hkey>" is added to the dirty set.
Returns the resulting availability (ms) as a string.
]]
local base = sp_base()
local p = ARGV[1]
local field = ARGV[2]
if field ~= 'cd' and field ~= 'gcd' then
  return redis.error_reply('proxy cooldown: field must be cd or gcd')
end
local key = base .. 'px:' .. p
redis.call('HSET', key, field, sp_int_str(sp_num(ARGV[3], 0)), 'pid', ARGV[4])
local cur = redis.call('HMGET', key, 'cd', 'gcd', 'al', 'mc')
local avail = sp_num(cur[1], 0)
local gcd = sp_num(cur[2], 0)
if gcd > avail then
  avail = gcd
end
local rk = base .. 'pxrdy'
local score = redis.call('ZSCORE', rk, p)
if score and avail > 0 then
  score = tonumber(score)
  if avail > score or (avail < score and sp_num(cur[3], 0) < sp_num(cur[4], 1)) then
    redis.call('ZADD', rk, 'XX', sp_score_str(avail), p)
  end
end
redis.call('SADD', base .. 'dirty', 'p' .. p)
return sp_int_str(avail)
