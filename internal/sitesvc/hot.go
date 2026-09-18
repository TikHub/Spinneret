package sitesvc

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

// breakerStateField is the state field of the breaker hash (spec §5.5).
const breakerStateField = "st"

// attachHotState fills AvailableIdentities (ZCOUNT of the ready queue up to
// now) and BreakerState (HGET st of the breaker hash, closed when missing) of
// groups of one site, using one pipelined round trip. All keys share the site
// hash tag.
func (s *Service) attachHotState(ctx context.Context, siteKey int64, groups []EndpointGroup) error {
	for i := range groups {
		groups[i].BreakerState = BreakerClosed
	}
	if s.rdb == nil || len(groups) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, redisTimeout)
	defer cancel()
	now := strconv.FormatInt(s.now().UnixMilli(), 10)
	cmds := make(rueidis.Commands, 0, 2*len(groups))
	for _, g := range groups {
		cmds = append(cmds,
			s.rdb.B().Zcount().Key(s.keys.Ready(siteKey, g.Key)).Min("-inf").Max(now).Build(),
			s.rdb.B().Hget().Key(s.keys.Breaker(siteKey, g.Key)).Field(breakerStateField).Build(),
		)
	}
	results := s.rdb.DoMulti(ctx, cmds...)
	for i := range groups {
		n, err := results[2*i].AsInt64()
		if err != nil {
			return apperr.Internal(fmt.Errorf("read ready queue of endpoint group %s: %w", groups[i].ID, err))
		}
		groups[i].AvailableIdentities = n
		st, err := results[2*i+1].ToString()
		switch {
		case rueidis.IsRedisNil(err):
		case err != nil:
			return apperr.Internal(fmt.Errorf("read breaker of endpoint group %s: %w", groups[i].ID, err))
		case st == BreakerOpen || st == BreakerHalfOpen || st == BreakerClosed:
			groups[i].BreakerState = st
		}
	}
	return nil
}
