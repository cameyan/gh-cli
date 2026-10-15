package artifact

import (
	"fmt"
	"testing"

	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
	"github.com/stretchr/testify/require"
)

func TestDigestContainerImageArtifact(t *testing.T) {
	expectedDigest := "1234567890abcdef"
	client := oci.MockClient{}
	url := "example.com/repo:tag"
	digestedArtifact, err := digestContainerImageArtifact(url, client)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("oci://%s", url), digestedArtifact.URL)
	require.Equal(t, expectedDigest, digestedArtifact.digest)
	require.Equal(t, "sha256", digestedArtifact.digestAlg)
}

func TestParseImageRefFailure(t *testing.T) {
	client := oci.ReferenceFailClient{}
	url := "example.com/repo:tag"
	_, err := digestContainerImageArtifact(url, client)
	require.Error(t, err)
}

func TestFetchImageFailure(t *testing.T) {
	testcase := []struct {
		name        string
		client      oci.Client
		expectedErr error
	}{
		{
			name:        "向仓库(registry)授权失败",
			client:      oci.AuthFailClient{},
			expectedErr: oci.ErrRegistryAuthz,
		},
		{
			name:        "因被拒绝而获取镜像(image)失败",
			client:      oci.DeniedClient{},
			expectedErr: oci.ErrDenied,
		},
	}

	for _, tc := range testcase {
		url := "example.com/repo:tag"
		_, err := digestContainerImageArtifact(url, tc.client)
		require.ErrorIs(t, err, tc.expectedErr)
	}
}
