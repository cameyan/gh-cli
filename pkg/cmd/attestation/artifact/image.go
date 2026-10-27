package artifact

import (
	"fmt"

	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
	"github.com/distribution/reference"
)

func digestContainerImageArtifact(url string, client oci.Client) (*DigestedArtifact, error) {
	// 尝试将 URL 解析为有效的仓库(registry)引用(reference)
	named, err := reference.Parse(url)
	if err != nil {
		// 无法解析为仓库(registry)引用(reference)
		return nil, fmt.Errorf("制品 %s 不是有效的仓库引用: %v", url, err)
	}

	digest, nameRef, err := client.GetImageDigest(named.String())

	if err != nil {
		return nil, err
	}

	return &DigestedArtifact{
		URL:       fmt.Sprintf("oci://%s", named.String()),
		digest:    digest.Hex,
		digestAlg: digest.Algorithm,
		nameRef:   nameRef,
	}, nil
}
