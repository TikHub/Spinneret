package analytics

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

// ClickHouse server error codes that mean "this query asked for too much",
// not "the system is broken". They become actionable client errors so the
// console can tell the operator to narrow the query instead of showing an
// internal error.
const (
	chCodeTooManyRows        = 158
	chCodeTimeoutExceeded    = 159
	chCodeTooManyBytes       = 160
	chCodeSocketTimeout      = 209
	chCodeMemoryLimit        = 241
	chCodeMemoryLimitPerUser = 291
	chCodeQueryWasCancelled  = 394
)

// chError converts a ClickHouse failure into an application error. Resource
// guards (memory, row/byte quotas) and timeouts become client-visible errors
// that name the remedy; everything else stays an internal error so that it is
// logged with its cause.
func chError(err error, what string) error {
	if err == nil {
		return nil
	}
	timeout := func() error {
		return apperr.New(connect.CodeDeadlineExceeded, apperr.ReasonQueryTimeout,
			"the analytics query timed out: narrow the time range or add filters").WithCause(err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return timeout()
	}
	var ex *clickhouse.Exception
	if errors.As(err, &ex) {
		switch int(ex.Code) {
		case chCodeMemoryLimit, chCodeMemoryLimitPerUser, chCodeTooManyRows, chCodeTooManyBytes:
			return apperr.ResourceExhausted(apperr.ReasonQueryTooLarge, 0,
				"the analytics query needs more resources than ClickHouse allows: narrow the time range or add filters").WithCause(err)
		case chCodeTimeoutExceeded, chCodeSocketTimeout, chCodeQueryWasCancelled:
			return timeout()
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}
