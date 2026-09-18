package observability

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// DefaultServiceName is used when SetupTracing receives an empty service name.
const DefaultServiceName = "spinneret"

// SetupTracing installs a global OpenTelemetry tracer provider exporting spans
// over OTLP/gRPC to endpoint, together with the W3C trace-context and baggage
// propagators, and returns a function that flushes and shuts it down.
//
// endpoint may be "host:port" (plaintext), "http://host:port" (plaintext) or
// "https://host:port" (TLS); any path is ignored. An empty endpoint disables
// tracing and returns a no-op shutdown function without touching globals.
// The resource carries service.name, service.version, the SDK attributes and
// OTEL_RESOURCE_ATTRIBUTES.
func SetupTracing(ctx context.Context, endpoint, serviceName, version string) (func(context.Context) error, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	opts, err := exporterOptions(endpoint)
	if err != nil {
		return nil, err
	}
	if serviceName == "" {
		serviceName = DefaultServiceName
	}
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil && !errors.Is(err, resource.ErrPartialResource) && !errors.Is(err, resource.ErrSchemaURLConflict) {
		return nil, fmt.Errorf("observability: build trace resource: %w", err)
	}
	if res == nil {
		res = resource.Empty()
	}
	exp, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("observability: create OTLP trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return func(ctx context.Context) error {
		if err := tp.Shutdown(ctx); err != nil {
			return fmt.Errorf("observability: shutdown tracer provider: %w", err)
		}
		return nil
	}, nil
}

// exporterOptions derives OTLP/gRPC exporter options from an endpoint string.
func exporterOptions(endpoint string) ([]otlptracegrpc.Option, error) {
	if !strings.Contains(endpoint, "://") {
		if strings.ContainsAny(endpoint, "/@ \t") {
			return nil, errors.New("observability: invalid OTLP endpoint (want host:port or an http(s) URL)")
		}
		return []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(endpoint),
			otlptracegrpc.WithInsecure(),
		}, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, errors.New("observability: invalid OTLP endpoint URL")
	}
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(u.Host)}
	switch strings.ToLower(u.Scheme) {
	case "http":
		opts = append(opts, otlptracegrpc.WithInsecure())
	case "https":
	default:
		return nil, fmt.Errorf("observability: unsupported OTLP endpoint scheme %q (want http or https)", u.Scheme)
	}
	return opts, nil
}
