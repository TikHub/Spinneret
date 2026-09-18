--[[
proxy health.lua — records one health-check observation on one site
(spec §5.4, §6.4).

KEYS[1] = "P:T:meta"
ARGV[1] = proxy hkey
ARGV[2] = observation value (100 success, 0 failure)
ARGV[3] = now (Unix ms)
ARGV[4] = EWMA alpha
ARGV[5] = score baseline
ARGV[6] = decay tau (ms)
ARGV[7] = failure streak reset window (ms); 0 disables the reset
ARGV[8] = "1" for a successful check, "0" for a failed one

Score: sc = ewma(decay(sc, sts, now), observation); sts = now; sn += 1.
Streak: failure → nf = (now - lf > reset ? 0 : nf) + 1, lf = now;
success → nf = floor(nf / 2).
Hashes that do not exist (proxy not materialized on the site) are left
untouched. Returns the new score as a string, or false when skipped.
]]
local base = sp_base()
local p = ARGV[1]
local key = base .. 'px:' .. p
if redis.call('EXISTS', key) == 0 then
  return false
end
local observation = sp_num(ARGV[2], 0)
local now = sp_num(ARGV[3], 0)
local alpha = sp_num(ARGV[4], 0.1)
local baseline = sp_num(ARGV[5], 70)
local tau = sp_num(ARGV[6], 0)
local reset_after = sp_num(ARGV[7], 0)
local ok = ARGV[8] == '1'

local cur = redis.call('HMGET', key, 'sc', 'sts', 'sn', 'nf', 'lf')
local score = sp_decay(sp_num(cur[1], baseline), sp_num(cur[2], now), now, baseline, tau)
score = sp_ewma(score, observation, alpha)
local samples = sp_num(cur[3], 0) + 1
local nfail = sp_num(cur[4], 0)
local lastfail = sp_num(cur[5], 0)
if ok then
  nfail = math.floor(nfail / 2)
else
  if reset_after > 0 and now - lastfail > reset_after then
    nfail = 0
  end
  nfail = nfail + 1
  lastfail = now
end
local sc = string.format('%.2f', score)
redis.call('HSET', key,
  'sc', sc,
  'sts', sp_int_str(now),
  'sn', sp_int_str(samples),
  'nf', sp_int_str(nfail),
  'lf', sp_int_str(lastfail))
redis.call('SADD', base .. 'dirty', 'p' .. p)
return sc
