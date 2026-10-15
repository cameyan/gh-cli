//go:build windows

package artifact

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeReference(t *testing.T) {
	testCases := []struct {
		name           string
		reference      string
		pathSeparator  rune
		expectedResult string
		expectedType   artifactType
		expectedError  bool
	}{
		{
			name:           "不带协议(scheme)的 Windows 文件引用(reference)",
			reference:      `c:\path\to\file`,
			pathSeparator:  '\\',
			expectedResult: `c:\path\to\file`,
			expectedType:   fileArtifactType,
			expectedError:  false,
		},
		{
			name:           "Windows 路径",
			reference:      "file:///C:/path/to/file",
			pathSeparator:  '\\',
			expectedResult: `C:\path\to\file`,
			expectedType:   fileArtifactType,
			expectedError:  false,
		},
		{
			name:           "带反斜杠的 Windows 路径",
			reference:      "file:///C:\\path\\to\\file",
			pathSeparator:  '\\',
			expectedResult: `C:\path\to\file`,
			expectedType:   fileArtifactType,
			expectedError:  false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, artifactType, err := normalizeReference(tc.reference, tc.pathSeparator)
			if tc.expectedError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedResult, result)
				require.Equal(t, tc.expectedType, artifactType)
			}
		})
	}
}
