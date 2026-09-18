package observability

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func restoreGlobalTracing(t *testing.T) {
	t.Helper()
	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
}

func TestSetupTracingDisabled(t *testing.T) {
	restoreGlobalTracing(t)
	before := otel.GetTracerProvider()
	for _, endpoint := range []string{"", "   "} {
		shutdown, err := SetupTracing(t.Context(), endpoint, "spinneret", "v0.1.0")
		require.NoError(t, err)
		require.NotNil(t, shutdown)
		require.NoError(t, shutdown(t.Context()))
	}
	require.Equal(t, before, otel.GetTracerProvider(), "disabled tracing must not install a provider")
}

func TestSetupTracingEnabled(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		service  string
	}{
		{name: "host port", endpoint: "127.0.0.1:4317", service: "spinneret-test"},
		{name: "http url", endpoint: "http://127.0.0.1:4317", service: ""},
		{name: "https url with path", endpoint: "https://collector.invalid:4317/v1/traces", service: "svc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreGlobalTracing(t)
			shutdown, err := SetupTracing(t.Context(), tt.endpoint, tt.service, "v0.1.0")
			require.NoError(t, err)
			require.NotNil(t, shutdown)
			tp, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
			require.True(t, ok, "SDK tracer provider must be installed")
			require.NotNil(t, tp)
			fields := otel.GetTextMapPropagator().Fields()
			require.Contains(t, fields, "traceparent")
			require.Contains(t, fields, "baggage")

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, shutdown(ctx))
		})
	}
}

func TestSetupTracingPartialResourceFromEnv(t *testing.T) {
	restoreGlobalTracing(t)
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "malformed-without-equals")
	shutdown, err := SetupTracing(t.Context(), "127.0.0.1:4317", "spinneret", "dev")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
}

func TestSetupTracingShutdownError(t *testing.T) {
	restoreGlobalTracing(t)
	shutdown, err := SetupTracing(t.Context(), "127.0.0.1:4317", "spinneret", "dev")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, shutdown(ctx))
	// A second shutdown reports the provider error, wrapped with context.
	err = shutdown(ctx)
	if err != nil {
		require.Contains(t, err.Error(), "shutdown tracer provider")
	}
}

func TestSetupTracingInvalidEndpoint(t *testing.T) {
	restoreGlobalTracing(t)
	tests := []struct {
		name     string
		endpoint string
		wantErr  string
	}{
		{name: "unsupported scheme", endpoint: "ftp://collector:4317", wantErr: `unsupported OTLP endpoint scheme "ftp"`},
		{name: "url without host", endpoint: "http://", wantErr: "invalid OTLP endpoint URL"},
		{name: "unparseable url", endpoint: "http://[::1", wantErr: "invalid OTLP endpoint URL"},
		{name: "path without scheme", endpoint: "collector:4317/v1", wantErr: "invalid OTLP endpoint"},
		{name: "credentials without scheme", endpoint: "user:pw@collector:4317", wantErr: "invalid OTLP endpoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shutdown, err := SetupTracing(t.Context(), tt.endpoint, "spinneret", "dev")
			require.Error(t, err)
			require.Nil(t, shutdown)
			require.Contains(t, err.Error(), tt.wantErr)
			require.NotContains(t, err.Error(), "pw@")
		})
	}
}

func TestExporterOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		endpoint string
		wantLen  int
	}{
		{endpoint: "otel:4317", wantLen: 2},
		{endpoint: "http://otel:4317", wantLen: 2},
		{endpoint: "HTTP://otel:4317", wantLen: 2},
		{endpoint: "https://otel:4317", wantLen: 1},
		{endpoint: "https://user:pw@otel:4317", wantLen: 1},
	}
	for _, tt := range tests {
		opts, err := exporterOptions(tt.endpoint)
		require.NoError(t, err, tt.endpoint)
		require.Len(t, opts, tt.wantLen, tt.endpoint)
	}
}
