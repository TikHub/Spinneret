package main

import (
	"bytes"
	"flag"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/cmd/internal/buildinfo"
)

func TestParseFlags(t *testing.T) {
	var stderr bytes.Buffer
	o, err := parseFlags([]string{"--role", "worker", "--migrate"}, &stderr)
	require.NoError(t, err)
	require.Equal(t, options{role: "worker", migrate: true}, o)

	o, err = parseFlags([]string{"-version"}, &stderr)
	require.NoError(t, err)
	require.True(t, o.showVersion)

	_, err = parseFlags([]string{"--help"}, &stderr)
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, stderr.String(), "--role all|api|worker")

	_, err = parseFlags([]string{"serve"}, &stderr)
	require.ErrorContains(t, err, "unexpected arguments: serve")
	_, err = parseFlags([]string{"--nope"}, &stderr)
	require.Error(t, err)
}

func TestWithRoleOverridesEnvironment(t *testing.T) {
	env := func(k string) (string, bool) {
		if k == "SPINNERET_ROLE" {
			return "api", true
		}
		return "x", true
	}
	v, ok := withRole(env, "")("SPINNERET_ROLE")
	require.True(t, ok)
	require.Equal(t, "api", v)
	v, _ = withRole(env, "worker")("SPINNERET_ROLE")
	require.Equal(t, "worker", v)
	v, _ = withRole(env, "worker")("SPINNERET_HTTP_ADDR")
	require.Equal(t, "x", v)
}

func TestRunExitCodes(t *testing.T) {
	noEnv := func(string) (string, bool) { return "", false }
	var stdout, stderr bytes.Buffer
	stderr.Reset()
	require.Equal(t, 0, run([]string{"-h"}, noEnv, &stdout, &stderr))

	stderr.Reset()
	require.Equal(t, 2, run([]string{"--bogus"}, noEnv, &stdout, &stderr))

	stderr.Reset()
	require.Equal(t, 1, run(nil, noEnv, &stdout, &stderr))
	require.Contains(t, stderr.String(), "invalid configuration")
	require.Contains(t, stderr.String(), "SPINNERET_DATABASE_URL is required")

	stderr.Reset()
	env := map[string]string{
		"SPINNERET_DATABASE_URL": "postgres://127.0.0.1:1/none?connect_timeout=1",
		"SPINNERET_REDIS_URL":    "redis://127.0.0.1:1/0",
		"SPINNERET_KEKS":         "k1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"SPINNERET_LOG_LEVEL":    "error",
	}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	require.Equal(t, 1, run([]string{"--role", "sideways"}, lookup, &stdout, &stderr))
	require.Contains(t, stderr.String(), "SPINNERET_ROLE must be one of all, api, worker")

	stderr.Reset()
	require.Equal(t, 1, run([]string{"--role", "api"}, lookup, &stdout, &stderr))
	require.Contains(t, stderr.String(), "startup failed: connect postgresql")
}

func TestRunPrintsBuildVersion(t *testing.T) {
	noEnv := func(string) (string, bool) { return "", false }
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, run([]string{"--version"}, noEnv, &stdout, &stderr))
	require.Equal(t, "spinneret-server "+buildinfo.Version()+"\n", stdout.String())
	require.NotEqual(t, "spinneret-server \n", stdout.String())
}
