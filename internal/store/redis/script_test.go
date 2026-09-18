package redis

import (
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // EVALSHA digest.
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
)

func uniqueBody(t *testing.T, body string) string {
	t.Helper()
	var b [8]byte
	_, err := rand.Read(b[:])
	require.NoError(t, err)
	// A random comment guarantees the digest is not cached on the shared server yet.
	return "-- " + hex.EncodeToString(b[:]) + "\n" + body
}

func TestNewScriptSource(t *testing.T) {
	require.NotEmpty(t, CommonLua)
	s := NewScript("demo", "return 1")
	require.Equal(t, "demo", s.Name)
	require.True(t, strings.HasPrefix(s.Source(), preludeHeader))
	require.True(t, strings.HasSuffix(s.Source(), "return 1"))
	sum := sha1.Sum([]byte(s.Source())) //nolint:gosec // EVALSHA digest.
	require.Equal(t, hex.EncodeToString(sum[:]), s.SHA1())

	multiline := NewScript("bad\nname", "return 2")
	require.Contains(t, multiline.Source(), "-- ==== script: bad name ====\nreturn 2")

	for _, fn := range []string{
		"sp_base", "sp_dnum", "sp_num", "sp_split", "sp_hs_raw", "sp_hs_unpack", "sp_hs_pack", "sp_hs_get",
		"sp_hs_set", "sp_decay", "sp_ewma", "sp_avail", "sp_push_all", "sp_rescore", "sp_quota_est",
		"sp_hmget_map", "sp_int_str", "sp_str", "sp_score_str", "sp_hs_cd_ru",
	} {
		require.Contains(t, CommonLua, "local function "+fn+"(", fn)
		require.Contains(t, preludeByName, fn, "%s has no --@sp section", fn)
	}

	// Every helper of the library is reachable from a body that names it, and a
	// body gets the transitive closure of what it names - nothing more.
	all := NewScript("all", strings.Join([]string{
		"sp_base", "sp_num", "sp_split", "sp_hs_raw", "sp_hs_unpack", "sp_hs_pack", "sp_hs_get", "sp_hs_set",
		"sp_decay", "sp_ewma", "sp_avail", "sp_push_all", "sp_rescore", "sp_quota_est", "sp_hmget_map",
	}, " "))
	for _, sec := range preludeSections {
		require.Contains(t, all.Source(), "local function "+sec.name+"(", sec.name)
	}

	// sp_hs_pack needs sp_num and therefore sp_dnum, but not the availability
	// rule, the ZSET helpers or the helpers its documentation only mentions.
	one := NewScript("one", "return sp_hs_pack({})")
	for _, want := range []string{"sp_dnum", "sp_num", "sp_hs_pack"} {
		require.Contains(t, one.Source(), "local function "+want+"(", want)
	}
	for _, unwanted := range []string{
		"sp_avail", "sp_push_all", "sp_rescore", "sp_hmget_map", "sp_ewma", "sp_base", "sp_hs_unpack", "sp_hs_raw",
	} {
		require.NotContains(t, one.Source(), "local function "+unwanted+"(", unwanted)
	}
	require.Less(t, len(one.Source()), len(all.Source()))

	// A helper named only in a comment is not pulled in, and a name that is not
	// a helper at all is ignored.
	cmtBody := "-- sp_avail is not used here\nlocal sp_local = 1\nreturn sp_local"
	cmt := NewScript("cmt", cmtBody)
	require.NotContains(t, cmt.Source(), "local function sp_avail(")
	require.Equal(t, preludeHeader+"\n-- ==== script: cmt ====\n"+cmtBody, cmt.Source())
}

func TestScriptExec(t *testing.T) {
	client, keys := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	body := uniqueBody(t, `
redis.call('SET', KEYS[1], ARGV[1])
return redis.call('GET', KEYS[1]) .. ':' .. sp_int_str(tonumber(ARGV[2]))
`)
	s := NewScript("exec", body)
	key := keys.Lock("script-exec")

	// First call: the digest is unknown to the server, EVALSHA fails with NOSCRIPT and Exec falls back to EVAL.
	got, err := s.Exec(ctx, client, []string{key}, []string{"v1", "1758011411962"}).ToString()
	require.NoError(t, err)
	require.Equal(t, "v1:1758011411962", got)

	exists, err := client.Do(ctx, client.B().ScriptExists().Sha1(s.SHA1()).Build()).AsIntSlice()
	require.NoError(t, err)
	require.Equal(t, []int64{1}, exists)

	// Second call runs through EVALSHA.
	got, err = s.Exec(ctx, client, []string{key}, []string{"v2", "7"}).ToString()
	require.NoError(t, err)
	require.Equal(t, "v2:7", got)
}

func TestScriptExecError(t *testing.T) {
	client, _ := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := NewScript("boom", uniqueBody(t, `return redis.error_reply('boom')`))
	err := s.Exec(ctx, client, nil, nil).Error()
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}

func TestScriptExecMulti(t *testing.T) {
	client, keys := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := NewScript("multi", uniqueBody(t, `return redis.call('INCRBY', KEYS[1], ARGV[1])`))
	require.Nil(t, s.ExecMulti(ctx, client))

	k1, k2 := keys.Lock("m1"), keys.Lock("m2")
	calls := []rueidis.LuaExec{
		{Keys: []string{k1}, Args: []string{"1"}},
		{Keys: []string{k2}, Args: []string{"10"}},
		{Keys: []string{k1}, Args: []string{"5"}},
	}
	// First batch exercises the NOSCRIPT → EVAL fallback for every call.
	res := s.ExecMulti(ctx, client, calls...)
	require.Len(t, res, 3)
	var got []int64
	for _, r := range res {
		n, err := r.AsInt64()
		require.NoError(t, err)
		got = append(got, n)
	}
	require.Equal(t, []int64{1, 10, 6}, got)

	// Second batch hits the cached digest.
	res = s.ExecMulti(ctx, client, calls...)
	got = got[:0]
	for _, r := range res {
		n, err := r.AsInt64()
		require.NoError(t, err)
		got = append(got, n)
	}
	require.Equal(t, []int64{7, 20, 12}, got)

	// Errors are reported per call.
	bad := NewScript("multi-bad", uniqueBody(t, `if ARGV[1] == 'x' then return redis.error_reply('bad call') end return 1`))
	res = bad.ExecMulti(ctx, client, rueidis.LuaExec{Args: []string{"ok"}}, rueidis.LuaExec{Args: []string{"x"}})
	require.NoError(t, res[0].Error())
	require.ErrorContains(t, res[1].Error(), "bad call")
}

func TestScriptCannotRedefineGlobals(t *testing.T) {
	client, _ := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Guard rail documenting why the prelude uses locals: scripts may not create globals.
	s := NewScript("global", uniqueBody(t, `function leaked_global() return 1 end return leaked_global()`))
	require.Error(t, s.Exec(ctx, client, nil, nil).Error())
}
