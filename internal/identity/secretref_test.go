package identity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretRefPaths(t *testing.T) {
	ct := compileSpec(t, richSpec())
	for _, path := range []string{"signing/../prod/db", "signing//token", "signing/token/", "signing/./token", "../x", "Upper/x"} {
		require.False(t, ValidSecretRefPath(path), path)
	}
	require.True(t, ValidSecretRefPath("signing/api_key.v2"))

	payload, err := ct.Normalize(map[string]any{"device_id": "d", "token": "signing/token", "signer": "signing/hmac_secret"})
	require.NoError(t, err)
	require.Equal(t, []string{"signing/hmac_secret", "signing/token"}, ct.SecretRefs(payload))
	require.Equal(t, map[string]string{"signer": "signing/hmac_secret", "token": "signing/token"}, ct.SecretRefFields(payload))
	require.Empty(t, ct.SecretRefs(map[string]any{"device_id": "d", "token": ""}))
}
