--[[
reap.lua (owner: internal/scheduler) - expires overdue leases in one batch
(spec §6.8 lease reaper). Requires lease_end.lua.

KEYS[1] = "P:T:meta" of the site.
ARGV: 1 now ms, 2 batch limit (<= 500), 3 late report window ms,
      4.. endpoint group layout (see ls_groups).

Returns {scanned, <10 fields per expired lease>...} with fields
  lease id, identity id, proxy id, group hkey, node, token id,
  abandoned 0|1 (no report was ingested "rv" nor processed "rc"), probe 0|1,
  identity hkey, proxy hkey
scanned counts every lease id read from lsexp (including stale entries that
were only removed), so scanned < limit means the backlog is drained.
]]

local base = sp_base()
local now = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local late = ARGV[3]

local lsexp = base .. 'lsexp'
local ids = redis.call('ZRANGEBYSCORE', lsexp, '-inf', now, 'LIMIT', 0, limit)
local out = {tostring(#ids)}
if #ids == 0 then
  return out
end
local groups, baselines = ls_groups(4)
for _, lid in ipairs(ids) do
  local v = redis.call('HMGET', base .. 'ls:' .. lid, unpack(LS_FIELDS))
  if v[LS_ST] ~= 'active' then
    redis.call('ZREM', lsexp, lid)
  else
    local x = sp_num(v[LS_X], now)
    ls_end(base, lid, v, 'expired', x, now, late, groups, baselines)
    local abandoned = '0'
    if sp_num(v[LS_RV], 0) == 0 and sp_num(v[LS_RC], 0) == 0 then
      abandoned = '1'
    end
    local n = #out
    out[n + 1] = lid
    out[n + 2] = v[LS_IID] or ''
    out[n + 3] = v[LS_PID] or ''
    out[n + 4] = v[LS_E] or ''
    out[n + 5] = v[LS_N] or ''
    out[n + 6] = v[LS_TK] or ''
    out[n + 7] = abandoned
    out[n + 8] = v[LS_PR] or '0'
    out[n + 9] = v[LS_I] or ''
    out[n + 10] = v[LS_P] or ''
  end
end
return out
