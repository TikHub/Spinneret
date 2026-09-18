--[[
site_paused.lua (owner: internal/breaker) — mirrors the site switch into the
hot-state meta hash (spec §5 "P:T:meta" field "paused").

KEYS[1]  P:T:meta
ARGV[1]  "1" paused | "0" running

The field is only written when the meta hash exists: an unbuilt site gets the
flag from PostgreSQL when the hot state is materialized, and creating a partial
meta hash here could make it look built. Returns 1 when written, 0 otherwise.
]]

if redis.call('EXISTS', KEYS[1]) == 0 then
  return 0
end
redis.call('HSET', KEYS[1], 'paused', ARGV[1] == '1' and '1' or '0')
return 1
