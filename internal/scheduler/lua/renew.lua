--[[
renew.lua (owner: internal/scheduler) - extends an active lease, bounded by
its lifetime cap (spec §6.1).

KEYS[1] = "P:T:meta" of the site.
ARGV: 1 now ms, 2 lease id, 3 extension ms (0 = the lease ttl),
      4 expected namespace id ("" = no check).

Returns
  {"OK", new expiry ms, identity id, group hkey, proxy id, node, token id, probe}
  {"UNKNOWN"} | {"RELEASED"} | {"EXPIRED"} | {"LIFETIME_EXCEEDED"}
]]

local base = sp_base()
local now = sp_dnum(ARGV[1])
local lid = ARGV[2]
local extend = tonumber(ARGV[3])
local ns = ARGV[4]

local lkey = base .. 'ls:' .. lid
local v = redis.call('HMGET', lkey, 'st', 'x', 'cap', 'ttl', 'mc', 'e', 'i', 'ns', 'iid', 'pid', 'n', 'tk', 'pr')
if not v[1] or (ns ~= '' and v[8] ~= ns) then
  return {'UNKNOWN'}
end
if v[1] == 'released' then
  return {'RELEASED'}
end
if v[1] ~= 'active' then
  return {'EXPIRED'}
end
local x = sp_num(v[2], 0)
local cap = sp_num(v[3], 0)
if now >= x then
  return {'EXPIRED'}
end
if x >= cap then
  return {'LIFETIME_EXCEEDED'}
end
if extend <= 0 then
  extend = sp_num(v[4], 0)
end
local nx = now + extend
if nx > cap then
  nx = cap
end
if nx < x then
  nx = x
end
redis.call('HSET', lkey, 'x', nx)
redis.call('ZADD', base .. 'lsexp', nx, lid)
if sp_num(v[5], 0) == 1 and v[7] then
  local idkey = base .. 'id:' .. v[7]
  local id = redis.call('HMGET', idkey, 'st', 'xl')
  if id[1] then
    if nx > sp_num(id[2], 0) then
      redis.call('HSET', idkey, 'xl', nx)
    end
    redis.call('ZADD', base .. 'rdy:' .. (v[6] or ''), 'XX', 'GT', nx, v[7])
  end
end
return {'OK', sp_int_str(nx), v[9] or '', v[6] or '', v[10] or '', v[11] or '', v[12] or '', v[13] or '0'}
