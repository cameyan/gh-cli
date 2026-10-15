package download

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"runtime"
	"testing"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"

	"github.com/stretchr/testify/require"
)

type MockStore struct {
	OnCreateMetadataFile func(artifactDigest string, attestationsResp []*api.Attestation) (string, error)
}

func (s *MockStore) createMetadataFile(artifact string, attestationsResp []*api.Attestation) (string, error) {
	return s.OnCreateMetadataFile(artifact, attestationsResp)
}

func OnCreateMetadataFileFailure(artifactDigest string, attestationsResp []*api.Attestation) (string, error) {
	return "", fmt.Errorf("创建受信任元数据(trusted metadata)文件失败")
}

func TestCreateJSONLinesFilePath(t *testing.T) {
	tempDir := t.TempDir()
	artifact, err := artifact.NewDigestedArtifact(oci.MockClient{}, "../test/data/sigstore-js-2.1.0.tgz", "sha512")
	require.NoError(t, err)

	var expectedFileName string
	if runtime.GOOS == "windows" {
		expectedFileName = fmt.Sprintf("%s-%s.jsonl", artifact.Algorithm(), artifact.Digest())
	} else {
		expectedFileName = fmt.Sprintf("%s.jsonl", artifact.DigestWithAlg())
	}

	testCases := []struct {
		name       string
		outputPath string
		expected   string
	}{
		{
			name:       "提供输出路径(output path)的情况",
			outputPath: tempDir,
			expected:   path.Join(tempDir, expectedFileName),
		},
		{
			name:       "提供嵌套输出路径(output path)的情况",
			outputPath: path.Join(tempDir, "subdir"),
			expected:   path.Join(tempDir, "subdir", expectedFileName),
		},
		{
			name:       "输出路径(output path)以斜杠开头的情况",
			outputPath: path.Join("/", tempDir, "subdir"),
			expected:   path.Join("/", tempDir, "subdir", expectedFileName),
		},
		{
			name:       "未提供输出路径(output path)的情况",
			outputPath: "",
			expected:   expectedFileName,
		},
	}

	for _, tc := range testCases {
		store := LiveStore{
			tc.outputPath,
		}

		actualPath := store.createJSONLinesFilePath(artifact.DigestWithAlg())
		require.Equal(t, tc.expected, actualPath)
	}
}

func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	counter := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		counter += 1
	}

	return counter, nil
}
