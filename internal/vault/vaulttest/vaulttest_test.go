package vaulttest_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/vault"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaulttest"
)

func TestNewCipher(t *testing.T) {
	a, b := vaulttest.NewCipher(t), vaulttest.NewCipher(t)
	require.Equal(t, vaulttest.DefaultKEKID, a.Provider().CurrentID())

	aad := vault.AAD("sec_1", "v1")
	s, err := a.Seal([]byte("hello"), aad)
	require.NoError(t, err)
	got, err := a.Open(s, aad)
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))

	_, err = b.Open(s, aad)
	require.ErrorIs(t, err, vault.ErrDecrypt, "each cipher must get its own random key")
}

func TestNewProvider(t *testing.T) {
	p := vaulttest.NewProvider(t, "k1", "k2")
	require.Equal(t, "k2", p.CurrentID())
	require.Equal(t, []string{"k1", "k2"}, p.IDs())
	require.Len(t, vaulttest.RandomKey(t), vault.KEKSize)
}
