package policy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutcomes(t *testing.T) {
	all := Outcomes()
	require.Len(t, all, outcomeCount)
	for i, o := range all {
		require.Equal(t, i, outcomeIndex(o))
		require.True(t, ValidOutcome(o))
	}
	require.False(t, ValidOutcome(""))
	require.False(t, ValidOutcome("SUCCESS"))
	require.Equal(t, -1, outcomeIndex("x"))

	tests := []struct {
		outcome string
		risk    bool
		failure bool
		blame   Blame
	}{
		{OutcomeSuccess, false, false, BlameNone},
		{OutcomeEmpty, false, true, BlameIdentity},
		{OutcomeRateLimited, true, true, BlameBoth},
		{OutcomeCaptcha, true, true, BlameIdentity},
		{OutcomeAuthInvalid, false, true, BlameIdentity},
		{OutcomeForbidden, true, true, BlameIdentity},
		{OutcomeBanned, true, true, BlameIdentity},
		{OutcomeProxyError, false, false, BlameProxy},
		{OutcomeNetworkError, false, false, BlameProxy},
		{OutcomeTargetError, false, false, BlameNone},
		{OutcomeClientError, false, false, BlameNone},
		{OutcomeUnknown, false, false, BlameNone},
		{"bogus", false, false, BlameNone},
	}
	for _, tc := range tests {
		t.Run(tc.outcome, func(t *testing.T) {
			require.Equal(t, tc.risk, IsRiskOutcome(tc.outcome))
			require.Equal(t, tc.failure, IsFailureOutcome(tc.outcome))
			require.Equal(t, tc.blame, DefaultBlame(tc.outcome))
		})
	}
}

func TestBlame(t *testing.T) {
	tests := []struct {
		blame    Blame
		valid    bool
		identity bool
		proxy    bool
	}{
		{BlameNone, true, false, false},
		{BlameIdentity, true, true, false},
		{BlameProxy, true, false, true},
		{BlameBoth, true, true, true},
		{"", false, false, false},
		{"all", false, false, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.blame), func(t *testing.T) {
			require.Equal(t, tc.valid, ValidBlame(tc.blame))
			require.Equal(t, tc.identity, tc.blame.Identity())
			require.Equal(t, tc.proxy, tc.blame.Proxy())
		})
	}
}

func TestErrorKinds(t *testing.T) {
	for _, k := range ErrorKinds() {
		require.True(t, ValidErrorKind(k))
	}
	require.Equal(t, []string{"timeout", "conn_reset", "conn_refused", "proxy_auth", "tls", "dns", "other"}, ErrorKinds())
	require.True(t, ValidErrorKind(ErrorKindOther))
	require.False(t, ValidErrorKind(""))
	require.False(t, ValidErrorKind("boom"))
	require.False(t, ValidErrorKind("Timeout"))
	// ErrorKinds returns a fresh slice: callers may not corrupt the set.
	ks := ErrorKinds()
	ks[0] = "mutated"
	require.Equal(t, ErrorKindTimeout, ErrorKinds()[0])
}
