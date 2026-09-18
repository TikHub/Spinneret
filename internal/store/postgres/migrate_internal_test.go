package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrationFileVersion(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		want    int64
		wantErr string
	}{
		{name: "valid", file: "00001_core.sql", want: 1},
		{name: "large", file: "20260917120000_add.sql", want: 20260917120000},
		{name: "no separator", file: "00001.sql", wantErr: "missing version prefix"},
		{name: "not numeric", file: "abc_core.sql", wantErr: "invalid version prefix"},
		{name: "zero", file: "0_core.sql", wantErr: "invalid version prefix"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := migrationFileVersion(tc.file)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
