--[[
proxy reset.lua — clears the proxy x site health statistics (spec §5.4).

KEYS[1] = "P:T:meta"
ARGV[1] = proxy hkey

Deletes the fields sc, sts, sn, nf and lf of "px:<hkey>" (a missing score reads
as the baseline) and adds "p<hkey>" to the dirty set. Returns 1 when the hash
exists, 0 otherwise (nothing is written).
]]
local base = sp_base()
local p = ARGV[1]
local key = base .. 'px:' .. p
if redis.call('EXISTS', key) == 0 then
  return 0
end
redis.call('HDEL', key, 'sc', 'sts', 'sn', 'nf', 'lf')
redis.call('SADD', base .. 'dirty', 'p' .. p)
return 1
