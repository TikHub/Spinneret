--[[
revert.lua (owner: internal/breaker) — reverts cooldowns applied during the
breaker window when a breaker opens (spec §0 revert_recent_cooldowns, §5).

KEYS[1]  P:T:meta
ARGV[1]  endpoint group hkey whose breaker opened
ARGV[2]  now (ms)
ARGV[3]  window (ms): only entries applied at or after now - window are reverted
ARGV[4]  mode: "endpoint" | "all"
ARGV[5]  maximum entries processed per set
ARGV[6]  comma-separated endpoint group hkeys of the site (mode "all": identity
         site-cooldown reverts rescore the identity on every group; groups the
         identity is not ready in are untouched because rescoring uses ZADD XX)

endpoint: for every member "<i>|<prevCd>|<prevNfail>" of "rcd:<eg>" in the
          window: lower hs cd back to prevCd and nfail back to prevNfail when
          the current values are higher, rescore "rdy:<eg>" and SADD dirty
          "e<eg>:<i>". Consumed members are removed.
all:      additionally for every member "<i>|<prevScd>" of "rcds" in the
          window: lower id scd back to prevScd when higher, rescore the
          identity on every group and SADD dirty "g<i>". Consumed members are
          removed.
Bans, expiry and quarantine are never touched.

Returns {endpoint identities reverted, site identities reverted}.
]]

local base = sp_base()
local eg = ARGV[1]
local now = sp_num(ARGV[2], 0)
local window = sp_num(ARGV[3], 0)
local mode = ARGV[4]
local limit = sp_num(ARGV[5], 10000)
if limit < 1 then limit = 1 end
local since = sp_int_str(now - window)
local dirty = base .. 'dirty'

local endpoint_reverted = 0
local site_reverted = 0

-- Identity x endpoint cooldowns.
local rcd = base .. 'rcd:' .. eg
local hs = base .. 'hs:' .. eg
local members = redis.call('ZRANGEBYSCORE', rcd, since, '+inf', 'LIMIT', 0, limit)
local touched = {}
local touched_list = {}
for _, m in ipairs(members) do
  local p = sp_split(m, '|')
  local i = p[1]
  if i ~= nil and i ~= '' then
    local raw = redis.call('HGET', hs, i)
    if raw then
      local h = sp_hs_unpack(raw, 0, now)
      local prev_cd = sp_num(p[2], 0)
      local prev_nfail = sp_num(p[3], 0)
      local changed = false
      if h.cd > prev_cd then
        h.cd = prev_cd
        changed = true
      end
      if h.nfail > prev_nfail then
        h.nfail = prev_nfail
        changed = true
      end
      if changed then
        sp_hs_set(base, eg, i, h)
        if not touched[i] then
          touched[i] = true
          touched_list[#touched_list + 1] = i
        end
      end
    end
  end
  redis.call('ZREM', rcd, m)
end
for _, i in ipairs(touched_list) do
  sp_rescore(base, eg, i)
  redis.call('SADD', dirty, 'e' .. eg .. ':' .. i)
end
endpoint_reverted = #touched_list

-- Identity x site cooldowns.
if mode == 'all' then
  local egs = sp_split(ARGV[6], ',')
  local rcds = base .. 'rcds'
  local site_members = redis.call('ZRANGEBYSCORE', rcds, since, '+inf', 'LIMIT', 0, limit)
  local site_touched = {}
  local site_list = {}
  for _, m in ipairs(site_members) do
    local p = sp_split(m, '|')
    local i = p[1]
    if i ~= nil and i ~= '' then
      local idk = base .. 'id:' .. i
      local scd = redis.call('HGET', idk, 'scd')
      if scd and sp_num(scd, 0) > sp_num(p[2], 0) then
        redis.call('HSET', idk, 'scd', sp_int_str(sp_num(p[2], 0)))
        if not site_touched[i] then
          site_touched[i] = true
          site_list[#site_list + 1] = i
        end
      end
    end
    redis.call('ZREM', rcds, m)
  end
  for _, i in ipairs(site_list) do
    for _, g in ipairs(egs) do
      if g ~= '' then
        sp_rescore(base, g, i)
      end
    end
    redis.call('SADD', dirty, 'g' .. i)
  end
  site_reverted = #site_list
end

return {endpoint_reverted, site_reverted}
