--[[
release.lua (owner: internal/scheduler) - ends one lease early (spec §6.1).
Requires lease_end.lua.

KEYS[1] = "P:T:meta" of the site.
ARGV: 1 now ms, 2 lease id, 3 late report window ms, 4 expected namespace id
      ("" = no check), 5 abort 0|1 (the lease was never delivered, see ls_end),
      6 n quota windows, n quota window ms values (used when aborting),
      then the endpoint group layout (see ls_groups).

Returns
  {"RELEASED", identity hkey, group hkey, proxy hkey, proxy id, identity id, node, token id, probe}
  {"ENDED", state}   the lease had already been released or had expired
  {"UNKNOWN"}        no such lease (or it belongs to another namespace)
]]

local base = sp_base()
local now = sp_dnum(ARGV[1])
local lid = ARGV[2]
local late = ARGV[3]
local ns = ARGV[4]
local abort_windows = nil
local nq = tonumber(ARGV[6]) or 0
if ARGV[5] == '1' then
  abort_windows = {}
  for q = 1, nq do
    abort_windows[q] = ARGV[6 + q]
  end
end

local v = ls_read(base, lid)
if not v[LS_ST] or (ns ~= '' and v[LS_NS] ~= ns) then
  return {'UNKNOWN'}
end
if v[LS_ST] ~= 'active' then
  return {'ENDED', v[LS_ST]}
end
ls_layout_at(7 + nq)
ls_end(base, lid, v, 'released', now, now, late, abort_windows)
return {'RELEASED', v[LS_I] or '', v[LS_E] or '', v[LS_P] or '', v[LS_PID] or '', v[LS_IID] or '',
  v[LS_N] or '', v[LS_TK] or '', v[LS_PR] or '0'}
