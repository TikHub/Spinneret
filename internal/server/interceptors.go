package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"connectrpc.com/connect"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/observability"
)

// errorInterceptor converts handler errors into Connect errors (keeping the
// Spinneret-Reason and retry headers of application errors), logs unexpected
// failures with their cause, and turns panics into internal errors.
type errorInterceptor struct {
	logger *slog.Logger
}

func newErrorInterceptor(logger *slog.Logger) *errorInterceptor {
	return &errorInterceptor{logger: logger}
}

func (i *errorInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (resp connect.AnyResponse, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				i.logger.Error("handler panic",
					slog.String("procedure", req.Spec().Procedure),
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())))
				resp, err = nil, apperr.ToConnect(apperr.Internal(fmt.Errorf("panic: %v", rec)))
			}
		}()
		resp, err = next(ctx, req)
		if err != nil {
			err = i.convert(ctx, req.Spec().Procedure, err)
		}
		return resp, err
	}
}

func (i *errorInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *errorInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				i.logger.Error("stream handler panic",
					slog.String("procedure", conn.Spec().Procedure),
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())))
				err = apperr.ToConnect(apperr.Internal(fmt.Errorf("panic: %v", rec)))
			}
		}()
		if err = next(ctx, conn); err != nil {
			err = i.convert(ctx, conn.Spec().Procedure, err)
		}
		return err
	}
}

func (i *errorInterceptor) convert(ctx context.Context, procedure string, err error) error {
	ce := apperr.ToConnect(err)
	if ce.Code() == connect.CodeInvalidArgument && ce.Meta().Get(apperr.HeaderReason) == "" {
		// Request validation (protovalidate) errors carry no reason of their own.
		ce.Meta().Set(apperr.HeaderReason, string(apperr.ReasonInvalidArgument))
	}
	if ce.Code() == connect.CodeInternal || ce.Code() == connect.CodeUnknown || ce.Code() == connect.CodeDataLoss {
		attrs := []any{slog.String("procedure", procedure), slog.Any("error", err)}
		if p, ok := authz.FromContext(ctx); ok {
			attrs = append(attrs, slog.String("actor", p.Actor()))
		}
		if !errors.Is(err, context.Canceled) {
			i.logger.Error("request failed", attrs...)
		}
	}
	return ce
}

// metricsInterceptor records request counts and latency per procedure.
type metricsInterceptor struct {
	metrics *observability.Metrics
}

func newMetricsInterceptor(m *observability.Metrics) *metricsInterceptor {
	return &metricsInterceptor{metrics: m}
}

func (i *metricsInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		resp, err := next(ctx, req)
		i.observe(req.Spec().Procedure, err, time.Since(start))
		return resp, err
	}
}

func (i *metricsInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *metricsInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		start := time.Now()
		err := next(ctx, conn)
		i.observe(conn.Spec().Procedure, err, time.Since(start))
		return err
	}
}

func (i *metricsInterceptor) observe(procedure string, err error, d time.Duration) {
	if i.metrics == nil {
		return
	}
	code := "ok"
	if err != nil {
		code = connect.CodeOf(err).String()
	}
	i.metrics.HTTPRequests.WithLabelValues(procedure, code).Inc()
	i.metrics.HTTPRequestDuration.WithLabelValues(procedure).Observe(d.Seconds())
}
