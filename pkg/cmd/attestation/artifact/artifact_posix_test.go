//go:build !windows

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
			name:           "不带协议(scheme)的文件引用(reference)",
			reference:      "/path/to/file",
			pathSeparator:  '/',
			expectedResult: "/path/to/file",
			expectedType:   fileArtifactType,
			expectedError:  false,
		},
		{
			name:           "带 %20 的文件(file)协议 URI",
			reference:      "file:///path/to/file%20with%20spaces",
			pathSeparator:  '/',
			expectedResult: "/path/to/file with spaces",
			expectedType:   fileArtifactType,
			expectedError:  false,
		},
		{
			name:           "不带协议(scheme)的 Windows 文件引用(reference)",
			reference:      `c:\path\to\file`,
			pathSeparator:  '\\',
			expectedResult: `c:\path\to\file`,
			expectedType:   fileArtifactType,
			expectedError:  false,
		},
		{
			name:           "带协议(scheme)的文件引用(reference)",
			reference:      "file:///path/to/file",
			pathSeparator:  '/',
			expectedResult: "/path/to/file",
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
		{
			name:           "OCI 引用(reference)",
			reference:      "oci://example.com/repo:tag",
			pathSeparator:  '/',
			expectedResult: "example.com/repo:tag",
			expectedType:   ociArtifactType,
			expectedError:  false,
		},
		{
			name:           "带摘要(digest)的 OCI 引用(reference)",
			reference:      "oci://example.com/repo@sha256:abcdef1234567890",
			pathSeparator:  '/',
			expectedResult: "example.com/repo@sha256:abcdef1234567890",
			expectedType:   ociArtifactType,
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
