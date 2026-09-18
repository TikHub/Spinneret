--[[
sync_maintenance.lua (owner: hotstate)

Small maintenance operations of the hot-state materialization, dispatched on
ARGV[1] so that one script body serves them all.

KEYS[1] = "P:T:meta"

"prune_ready" eg types identities...
    Removes identities from "rdy:<eg>" whose identity hash is missing, whose
    state is neither active nor pending, or whose type name is not in types
    (newline-separated names). Candidates are verified against the live hash
    so concurrent synchronizations are never undone. Returns the removed count.

"prune_proxies" proxies...
    Removes proxies from "pxrdy" whose hash is missing or not active.
    Returns the removed count.

"remove_proxies" proxies...
    Deletes "px:<p>" and "xa:p:<p>" and removes the proxies from "pxrdy".
    Returns the number of proxies that had a hash or a queue membership.

"clear_bindings" identity proxy [identity proxy ...]
    Sets "px" to "" on identity hashes whose bound proxy equals the given
    proxy hkey. Returns the number of cleared bindings.

"drop_groups" egs...
    Deletes the per-group keys of endpoint groups that no longer exist
    ("rdy", "hs", "brk", "rcd", "rr") and removes them from "brko" and "aeg".
    Returns the number of groups processed.
]]

local base = sp_base()
local op = ARGV[1]
local n = 0

if op == 'prune_ready' then
  local rk = base .. 'rdy:' .. ARGV[2]
  local types = {}
  for _, name in ipairs(sp_split(ARGV[3], '\n')) do
    types[name] = true
  end
  for idx = 4, #ARGV do
    local i = ARGV[idx]
    local v = redis.call('HMGET', base .. 'id:' .. i, 'st', 'ty')
    local st = v[1]
    local keep = (st == 'active' or st == 'pending') and v[2] and types[v[2]]
    if not keep then
      n = n + redis.call('ZREM', rk, i)
    end
  end
elseif op == 'prune_proxies' then
  local rk = base .. 'pxrdy'
  for idx = 2, #ARGV do
    local p = ARGV[idx]
    if redis.call('HGET', base .. 'px:' .. p, 'st') ~= 'active' then
      n = n + redis.call('ZREM', rk, p)
    end
  end
elseif op == 'remove_proxies' then
  local rk = base .. 'pxrdy'
  for idx = 2, #ARGV do
    local p = ARGV[idx]
    local gone = redis.call('DEL', base .. 'px:' .. p)
    redis.call('DEL', base .. 'xa:p:' .. p)
    gone = gone + redis.call('ZREM', rk, p)
    if gone > 0 then
      n = n + 1
    end
  end
elseif op == 'clear_bindings' then
  local idx = 2
  while idx + 1 <= #ARGV do
    local idk = base .. 'id:' .. ARGV[idx]
    if redis.call('HGET', idk, 'px') == ARGV[idx + 1] then
      redis.call('HSET', idk, 'px', '')
      n = n + 1
    end
    idx = idx + 2
  end
elseif op == 'drop_groups' then
  for idx = 2, #ARGV do
    local eg = ARGV[idx]
    -- UNLINK frees large ready queues and health hashes in the background.
    redis.call('UNLINK', base .. 'rdy:' .. eg, base .. 'hs:' .. eg, base .. 'brk:' .. eg,
      base .. 'rcd:' .. eg, base .. 'rr:' .. eg)
    redis.call('SREM', base .. 'brko', eg)
    redis.call('ZREM', base .. 'aeg', eg)
    n = n + 1
  end
else
  return redis.error_reply('sync_maintenance: unknown operation')
end

return n
