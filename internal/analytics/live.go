package analytics

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/catalog"
)

// reportConsumerGroup is the consumer group of the report stream workers
// (worker.ConsumerGroup).
const reportConsumerGroup = "workers"

// siteLive is the live Redis state of one site used by the overview.
type siteLive struct {
	// ready maps endpoint group hot-state keys to the number of identities
	// available now.
	ready    map[int64]int64
	open     int
	halfOpen int
}

// liveRef maps a pipelined command back to its site (and endpoint group for
// ready counts; nil for the open breaker set).
type liveRef struct {
	site  int
	group *catalog.EndpointGroup
}

// readLive reads ready counts and breaker states of the sites in two
// pipelined round trips: ZCOUNT rdy:<eg> -inf now per endpoint group and
// SMEMBERS brko per site, then HGET brk:<eg> st per non-closed breaker of a
// known endpoint group.
func (s *Service) readLive(ctx context.Context, sites []*catalog.Site, now time.Time) ([]siteLive, error) {
	out := make([]siteLive, len(sites))
	if len(sites) == 0 {
		return out, nil
	}
	maxScore := itoa(now.UnixMilli())
	var (
		cmds rueidis.Commands
		refs []liveRef
	)
	for i, site := range sites {
		out[i].ready = make(map[int64]int64, len(site.GroupsByID))
		for _, g := range site.GroupsByID {
			cmds = append(cmds, s.rdb.B().Zcount().Key(s.keys.Ready(site.Key, g.Key)).Min("-inf").Max(maxScore).Build())
			refs = append(refs, liveRef{site: i, group: g})
		}
		cmds = append(cmds, s.rdb.B().Smembers().Key(s.keys.OpenBreakers(site.Key)).Build())
		refs = append(refs, liveRef{site: i})
	}

	var (
		brkCmds rueidis.Commands
		brkRefs []int
	)
	for j, res := range s.rdb.DoMulti(ctx, cmds...) {
		ref := refs[j]
		site := sites[ref.site]
		if ref.group != nil {
			n, err := res.AsInt64()
			if err != nil {
				return nil, fmt.Errorf("count ready identities of site %s group %s: %w", site.ID, ref.group.ID, err)
			}
			out[ref.site].ready[ref.group.Key] = n
			continue
		}
		members, err := res.AsStrSlice()
		if err != nil {
			return nil, fmt.Errorf("read open breakers of site %s: %w", site.ID, err)
		}
		for _, m := range members {
			key, err := strconv.ParseInt(m, 10, 64)
			if err != nil {
				s.logger.Debug("ignoring malformed open breaker set member",
					slog.String("site_id", site.ID), slog.String("member", m))
				continue
			}
			if _, known := site.GroupsByKey[key]; !known {
				continue
			}
			brkCmds = append(brkCmds, s.rdb.B().Hget().Key(s.keys.Breaker(site.Key, key)).Field(fieldState).Build())
			brkRefs = append(brkRefs, ref.site)
		}
	}
	if len(brkCmds) == 0 {
		return out, nil
	}
	for j, res := range s.rdb.DoMulti(ctx, brkCmds...) {
		state, err := res.ToString()
		if err != nil && !rueidis.IsRedisNil(err) {
			return nil, fmt.Errorf("read breaker state of site %s: %w", sites[brkRefs[j]].ID, err)
		}
		switch state {
		case breakerOpen:
			out[brkRefs[j]].open++
		case breakerHalfOpen:
			out[brkRefs[j]].halfOpen++
		}
	}
	return out, nil
}

// streamsPending returns the report backlog summed over every shard: for the
// worker consumer group, entries delivered but not acknowledged (pending)
// plus entries not yet delivered (lag). The whole stream length is used when
// the group does not exist yet or Redis cannot compute the lag (entries were
// trimmed or deleted past the group position).
func (s *Service) streamsPending(ctx context.Context) (int64, error) {
	cmds := make(rueidis.Commands, 0, 2*s.reportShards)
	for shard := range s.reportShards {
		stream := s.keys.Stream(shard)
		cmds = append(cmds,
			s.rdb.B().Xlen().Key(stream).Build(),
			s.rdb.B().XinfoGroups().Key(stream).Build())
	}
	results := s.rdb.DoMulti(ctx, cmds...)
	var total int64
	for shard := range s.reportShards {
		length, err := results[2*shard].AsInt64()
		if err != nil {
			return 0, fmt.Errorf("read length of report stream shard %d: %w", shard, err)
		}
		if length == 0 {
			continue
		}
		groups, err := results[2*shard+1].ToArray()
		if err != nil {
			return 0, fmt.Errorf("read consumer groups of report stream shard %d: %w", shard, err)
		}
		backlog, err := groupBacklog(groups, length)
		if err != nil {
			return 0, fmt.Errorf("parse consumer groups of report stream shard %d: %w", shard, err)
		}
		total += backlog
	}
	return total, nil
}

// groupBacklog computes the backlog of the worker consumer group from an
// XINFO GROUPS reply.
func groupBacklog(groups []rueidis.RedisMessage, length int64) (int64, error) {
	for i := range groups {
		info, err := groups[i].AsMap()
		if err != nil {
			return 0, err
		}
		nameMsg, ok := info["name"]
		if !ok {
			continue
		}
		name, err := nameMsg.ToString()
		if err != nil {
			return 0, err
		}
		if name != reportConsumerGroup {
			continue
		}
		var pending int64
		if msg, ok := info["pending"]; ok {
			if pending, err = msg.AsInt64(); err != nil {
				return 0, err
			}
		}
		lagMsg, ok := info["lag"]
		if !ok {
			// Servers before Redis 7 report no lag: pending is the best estimate.
			return pending, nil
		}
		if lagMsg.IsNil() {
			return length, nil
		}
		lag, err := lagMsg.AsInt64()
		if err != nil {
			return 0, err
		}
		return pending + lag, nil
	}
	return length, nil
}
