-- acquire_write.lua: lease writes of acquire.lua (spec §5.3, §6.1 step 6).

-- quota_write counts the acquired request in every quota window.
local function quota_write(c)
  local vals = c.q
  local args = {}
  for q = 1, C.nq do
    local w = C.qwin[q]
    local idx = math.floor(now / w)
    local cidx = sp_num(vals[3 * q - 2], -1)
    local n = sp_num(vals[3 * q - 1], 0)
    local p = sp_num(vals[3 * q], 0)
    if cidx == idx then
      n = n + 1
    else
      if cidx == idx - 1 then
        p = n
      else
        p = 0
      end
      n = 1
    end
    args[#args + 1] = C.qfields[3 * q - 2]
    args[#args + 1] = sp_int_str(idx)
    args[#args + 1] = C.qfields[3 * q - 1]
    args[#args + 1] = sp_int_str(n)
    args[#args + 1] = C.qfields[3 * q]
    args[#args + 1] = sp_int_str(p)
  end
  local qkey = base .. 'q:' .. eg .. ':' .. c.i
  redis.call('HSET', qkey, unpack(args))
  redis.call('PEXPIRE', qkey, sp_int_str(2 * C.qmax))
end

-- write_lease writes one lease (spec §5.3, §6.1 step 6) and appends its
-- fields to out.
local function write_lease(c, prefix, proxy, sticky_hit)
  local i = c.i
  local idkey = base .. 'id:' .. i
  local lid = prefix .. string.format('%02x', c.n % C.shards)
  local x = now + C.ttl
  local cap = now + C.lifetime
  if x > cap then
    x = cap
  end
  local p, pid = '', ''
  if proxy then
    p = proxy.p
    pid = proxy.pid
  end
  local lkey = base .. 'ls:' .. lid
  local pr = '0'
  if probe then
    pr = '1'
  end
  local fields = {
    'i', i, 'iid', c.iid, 'e', eg, 'p', p, 'pid', pid, 'n', C.node, 'tk', C.token,
    'ns', C.ns, 'sk', C.session, 'a', sp_int_str(now), 'x', sp_int_str(x), 'cap', sp_int_str(cap),
    'ttl', sp_int_str(C.ttl), 'st', 'active', 'pr', pr, 'mc', sp_int_str(C.mc), 'ri', sp_int_str(C.ri),
    'ra', C.ra, 'rs', C.rs, 'rc', '0',
  }
  -- An "acquired" reuse anchor raising the reuse value keeps the previous
  -- value ("pru") so that aborting an undelivered lease can restore it.
  if C.ra == 'a' and C.ri > 0 then
    local prev = c.hs.ru
    if C.rs == 's' then
      prev = c.sru
    end
    if now + C.ri > prev then
      fields[#fields + 1] = 'pru'
      fields[#fields + 1] = sp_int_str(prev)
    end
  end
  -- While active the hash has no TTL: the reaper resolves the identity and
  -- proxy of an overdue lease only from it. ls_end sets the TTL.
  redis.call('HSET', lkey, unpack(fields))
  redis.call('ZADD', base .. 'lsexp', sp_int_str(x), lid)

  local al = redis.call('HINCRBY', idkey, 'al', 1)
  local upd = {'lu', sp_int_str(now)}
  local xl = 0
  if al > 1 then
    xl = c.xl
  end
  if C.mc == 1 then
    if x > xl then
      xl = x
    end
    upd[#upd + 1] = 'xl'
    upd[#upd + 1] = sp_int_str(xl)
  end
  local sru = c.sru
  if C.ra == 'a' and C.ri > 0 and C.rs == 's' and now + C.ri > sru then
    sru = now + C.ri
    upd[#upd + 1] = 'sru'
    upd[#upd + 1] = sp_int_str(sru)
  end
  local rebound, rbn = '0', c.rbn
  if c.rbd ~= C.today then
    rbn = 0
  end
  if proxy and proxy.bind then
    upd[#upd + 1] = 'px'
    upd[#upd + 1] = p
    rebound = 'b'
    if proxy.rebound then
      rbn = rbn + 1
      rebound = 'r'
      upd[#upd + 1] = 'rbd'
      upd[#upd + 1] = C.today
      upd[#upd + 1] = 'rbn'
      upd[#upd + 1] = sp_int_str(rbn)
    end
  end
  redis.call('HSET', idkey, unpack(upd))

  local hs = c.hs
  hs.lu = now
  if C.ra == 'a' and C.ri > 0 and C.rs == 'e' and now + C.ri > hs.ru then
    hs.ru = now + C.ri
  end
  sp_hs_set(base, eg, i, hs)
  if C.nq > 0 then
    quota_write(c)
  end

  if proxy then
    local pal = redis.call('HINCRBY', base .. 'px:' .. p, 'al', 1)
    if pal >= proxy.mc then
      local ps = short_push
      if proxy.mc == 1 then
        ps = x
      end
      redis.call('ZADD', pxrdy, 'XX', 'GT', sp_int_str(ps), p)
    end
  end

  local av = now
  if hs.cd > av then av = hs.cd end
  if hs.ru > av then av = hs.ru end
  if c.scd > av then av = c.scd end
  if sru > av then av = sru end
  if xl > av then av = xl end
  if c.acd > av then av = c.acd end
  if al >= c.lim and short_push > av then av = short_push end
  redis.call('ZADD', rdy, 'XX', sp_score_str(av), i)

  if stkkey then
    redis.call('SET', stkkey, i, 'PX', sp_int_str(C.sticky_ttl))
  end
  redis.call('SADD', base .. 'dirty', 'e' .. eg .. ':' .. i, 'g' .. i)
  if probe then
    redis.call('HINCRBY', brkkey, 'hc', 1)
  end

  local sticky = '0'
  if sticky_hit then
    sticky = '1'
  end
  local n = #out
  out[n + 1] = lid
  out[n + 2] = c.iid
  out[n + 3] = c.ty
  out[n + 4] = c.pv
  out[n + 5] = c.tv
  out[n + 6] = sp_int_str(x)
  out[n + 7] = pr
  out[n + 8] = sticky
  out[n + 9] = rebound
  out[n + 10] = p
  out[n + 11] = pid
  out[n + 12] = c.st
  out[n + 13] = i
  out[n + 14] = sp_int_str(rbn)
end
