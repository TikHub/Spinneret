//go:build perf

package perf

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/rueidis"

	storeredis "github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// dataset is a synthetic site seeded straight into the hot-state key schema
// (spec §5), bypassing PostgreSQL and internal/hotstate. Only the keys the
// hot-path scripts read are written.
type dataset struct {
	prefix     string
	keys       storeredis.Keys
	siteKey    int64
	groups     []int64 // endpoint group hkeys, groups[0] is the benched one
	identities int64   // identity hkeys are 1..identities
	// groups[1] is the "half filtered" pool: the packed health entry of every
	// odd identity carries a cooldown in the far future, so evaluate() rejects
	// it with a "push" verdict. groups[0] is the clean pool, where the first
	// candidate always passes. The cooldown lives in hs:<eg>, so it filters in
	// groups[1] only and leaves groups[0] untouched.
	filtered bool
	// epoch is the fixed "now" the benchmarks pass to the scripts.
	epoch int64
	typ   string
	ns    string
}

const (
	dsTypeName  = "perf_cookie"
	dsNamespace = "ns_perf"
	dsSiteKey   = int64(1)
	dsFirstEG   = int64(1000)
	// seedChunk bounds the member count of one seeding command.
	seedChunk = 2000
)

// newDataset seeds a site and registers its removal.
func newDataset(tb testing.TB, e *env, identities int64, groups int, filtered, cleanup bool) *dataset {
	tb.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		tb.Fatalf("rand: %v", err)
	}
	ds := &dataset{
		prefix:     "pf" + hex.EncodeToString(b[:]),
		siteKey:    dsSiteKey,
		identities: identities,
		filtered:   filtered,
		epoch:      time.Now().UnixMilli(),
		typ:        dsTypeName,
		ns:         dsNamespace,
	}
	ds.keys = storeredis.NewKeys(ds.prefix)
	for g := 0; g < groups; g++ {
		ds.groups = append(ds.groups, dsFirstEG+int64(g))
	}

	start := time.Now()
	ds.seed(tb, e)
	fmt.Fprintf(os.Stderr, "seeded %s: %d identities x %d groups in %s\n",
		ds.prefix, identities, groups, time.Since(start).Round(time.Millisecond))

	if cleanup && !*flagKeep {
		tb.Cleanup(func() { ds.drop(e) })
	}
	return ds
}

// base is the site key base "P:{s1}:".
func (d *dataset) base() string { return d.keys.SiteBase(d.siteKey) }

// meta is KEYS[1] of every site script.
func (d *dataset) meta() string { return d.keys.SiteMeta(d.siteKey) }

// eg returns the clean endpoint group hkey (every candidate passes).
func (d *dataset) eg() int64 { return d.groups[0] }

// egMixed returns the endpoint group where half the candidates are filtered.
func (d *dataset) egMixed() int64 { return d.groups[1] }

// egIdle returns an endpoint group no acquire block samples. Blocks whose
// writes would change how acquire behaves (a cooldown on an identity, a breaker
// that trips) use it, so benchmark order cannot change another block's numbers.
func (d *dataset) egIdle() int64 { return d.groups[2] }

// seed writes the meta hash, the identity hashes, the ready ZSETs and the
// packed health entries of the benched group.
func (d *dataset) seed(tb testing.TB, e *env) {
	tb.Helper()
	c := e.client
	must := func(res rueidis.RedisResult) {
		if err := res.Error(); err != nil {
			tb.Fatalf("seed: %v", err)
		}
	}
	must(c.Do(e.ctx, c.B().Hset().Key(d.meta()).FieldValue().
		FieldValue("ns", d.ns).FieldValue("site", "perf").
		FieldValue("built", "1").FieldValue("paused", "0").Build()))

	// Identity hashes, written in pipelined batches.
	var (
		wg   sync.WaitGroup
		errs = make(chan error, 8)
	)
	batch := make(rueidis.Commands, 0, seedChunk)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		cmds := batch
		batch = make(rueidis.Commands, 0, seedChunk)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, res := range c.DoMulti(e.ctx, cmds...) {
				if err := res.Error(); err != nil {
					select {
					case errs <- err:
					default:
					}
				}
			}
		}()
	}
	act := strconv.FormatInt(d.epoch-86_400_000, 10)
	future := strconv.FormatInt(d.epoch+3_600_000, 10)
	for i := int64(1); i <= d.identities; i++ {
		id := strconv.FormatInt(i, 10)
		b := c.B().Hset().Key(d.keys.Identity(d.siteKey, i)).FieldValue().
			FieldValue("iid", "idt_"+id).FieldValue("st", "active").FieldValue("ty", d.typ).
			FieldValue("tv", "1").FieldValue("pv", "1").FieldValue("al", "0").FieldValue("xl", "0").
			FieldValue("acc", "").FieldValue("act", act).FieldValue("px", "").FieldValue("rg", "us").
			FieldValue("rbd", "").FieldValue("rbn", "0").FieldValue("lu", "0")
		batch = append(batch, b.FieldValue("scd", "0").FieldValue("sru", "0").Build())
		if len(batch) >= seedChunk {
			flush()
		}
	}
	flush()

	// Ready ZSETs and packed health entries.
	for gi, eg := range d.groups {
		rdy := d.keys.Ready(d.siteKey, eg)
		hs := d.keys.Health(d.siteKey, eg)
		for lo := int64(1); lo <= d.identities; lo += seedChunk {
			hi := min(lo+seedChunk-1, d.identities)
			z := c.B().Zadd().Key(rdy).ScoreMember()
			for i := lo; i <= hi; i++ {
				z = z.ScoreMember(0, strconv.FormatInt(i, 10))
			}
			batch = append(batch, z.Build())
			if gi <= 1 {
				// Only the two benched groups get warmed health entries; the
				// other groups exist so lease_end has a realistic client
				// layout. In groups[1] the odd identities carry a cooldown, so
				// half of every sampled candidate set is filtered.
				h := c.B().Hset().Key(hs).FieldValue()
				for i := lo; i <= hi; i++ {
					cd := "0"
					if gi == 1 && d.filtered && i%2 == 1 {
						cd = future
					}
					h = h.FieldValue(strconv.FormatInt(i, 10),
						fmt.Sprintf("%.2f|%d|%d|0|0|%s|0|%d", 70.0+float64(i%20), d.epoch-60_000, 5+i%50, cd, d.epoch-120_000))
				}
				batch = append(batch, h.Build())
			}
			flush()
		}
	}
	flush()
	wg.Wait()
	select {
	case err := <-errs:
		tb.Fatalf("seed: %v", err)
	default:
	}
}

// restore puts the ready ZSET of the benched group and the identity lease
// counters back to their seeded values and removes the leases a block wrote.
// It uses plain commands only, so the SLOWLOG script filter stays clean.
func (d *dataset) restore(tb testing.TB, e *env, leaseIDs []string, identityKeys []int64) {
	tb.Helper()
	c := e.client
	var cmds rueidis.Commands
	for _, eg := range d.groups[:min(2, len(d.groups))] {
		for lo := int64(1); lo <= d.identities; lo += seedChunk {
			hi := min(lo+seedChunk-1, d.identities)
			z := c.B().Zadd().Key(d.keys.Ready(d.siteKey, eg)).ScoreMember()
			for i := lo; i <= hi; i++ {
				z = z.ScoreMember(0, strconv.FormatInt(i, 10))
			}
			cmds = append(cmds, z.Build())
		}
	}
	seen := make(map[int64]bool, len(identityKeys))
	for _, i := range identityKeys {
		if seen[i] {
			continue
		}
		seen[i] = true
		cmds = append(cmds, c.B().Hset().Key(d.keys.Identity(d.siteKey, i)).FieldValue().
			FieldValue("al", "0").FieldValue("xl", "0").Build())
		cmds = append(cmds, c.B().Hdel().Key(d.keys.Identity(d.siteKey, i)).Field("xg").Build())
	}
	if len(leaseIDs) > 0 {
		del := c.B().Del().Key()
		for _, id := range leaseIDs {
			del = del.Key(d.keys.Lease(d.siteKey, id))
		}
		cmds = append(cmds, del.Build())
		zr := c.B().Zrem().Key(d.keys.LeaseExpiry(d.siteKey)).Member()
		for _, id := range leaseIDs {
			zr = zr.Member(id)
		}
		cmds = append(cmds, zr.Build())
	}
	cmds = append(cmds, c.B().Del().Key(d.keys.Dirty(d.siteKey)).Build())
	for _, res := range c.DoMulti(e.ctx, cmds...) {
		if err := res.Error(); err != nil && !rueidis.IsRedisNil(err) {
			tb.Fatalf("restore: %v", err)
		}
	}
}

// drop removes every key of the dataset.
func (d *dataset) drop(e *env) {
	c := e.client
	var cursor uint64
	for {
		entry, err := c.Do(e.ctx, c.B().Scan().Cursor(cursor).Match(d.prefix+":*").Count(5000).Build()).AsScanEntry()
		if err != nil {
			fmt.Fprintf(os.Stderr, "drop scan: %v\n", err)
			return
		}
		for lo := 0; lo < len(entry.Elements); lo += 500 {
			hi := min(lo+500, len(entry.Elements))
			if err := c.Do(e.ctx, c.B().Unlink().Key(entry.Elements[lo:hi]...).Build()).Error(); err != nil {
				fmt.Fprintf(os.Stderr, "drop unlink: %v\n", err)
			}
		}
		cursor = entry.Cursor
		if cursor == 0 {
			return
		}
	}
}
