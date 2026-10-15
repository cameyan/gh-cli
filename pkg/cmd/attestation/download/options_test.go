package download

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAreFlagsValid(t *testing.T) {
	tests := []struct {
		name  string
		limit int
	}{
		{
			name:  "limit 过低",
			limit: 0,
		},
		{
			name:  "limit 过高",
			limit: 1001,
		},
	}
	for _, tc := range tests {
		opts := Options{
			Limit: tc.limit,
		}

		err := opts.AreFlagsValid()
		require.Error(t, err)
		expectedErrMsg := fmt.Sprintf("不允许使用 limit %d，必须介于 1 和 1000 之间", tc.limit)
		require.ErrorContains(t, err, expectedErrMsg)
	}
}
