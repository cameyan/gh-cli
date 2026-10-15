package artifact

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
)

type artifactType int

const (
	ociArtifactType artifactType = iota
	fileArtifactType
)

// DigestedArtifact 对被验证的软件制品(artifact)进行了抽象
type DigestedArtifact struct {
	URL       string
	digest    string
	digestAlg string
	nameRef   name.Reference
}

func normalizeReference(reference string, pathSeparator rune) (normalized string, artifactType artifactType, err error) {
	switch {
	case strings.HasPrefix(reference, "oci://"):
		return reference[6:], ociArtifactType, nil
	case strings.HasPrefix(reference, "file://"):
		uri, err := url.ParseRequestURI(reference)
		if err != nil {
			return "", 0, fmt.Errorf("解析引用(reference) URI 失败: %v", err)
		}
		var path string
		if pathSeparator == '/' {
			// Unix 路径和 URI 一样使用正斜杠，因此无需修改
			path = uri.Path
		} else {
			// Windows 路径应规范化为使用反斜杠
			path = strings.ReplaceAll(uri.Path, "/", string(pathSeparator))
			// 如果 Windows 路径带有前导斜杠，则将其移除
			if strings.HasPrefix(path, string(pathSeparator)) {
				path = path[1:]
			}
		}
		return filepath.Clean(path), fileArtifactType, nil
	}
	// 将其他任何引用(reference)都视为本地文件路径
	return filepath.Clean(reference), fileArtifactType, nil
}

func NewDigestedArtifactForRelease(digest string, digestAlg string) (artifact *DigestedArtifact) {
	return &DigestedArtifact{
		digest:    digest,
		digestAlg: digestAlg,
	}
}

func NewDigestedArtifact(client oci.Client, reference, digestAlg string) (artifact *DigestedArtifact, err error) {
	normalized, artifactType, err := normalizeReference(reference, os.PathSeparator)
	if err != nil {
		return nil, err
	}
	if artifactType == ociArtifactType {
		// TODO: 是否应允许为 OCI 制品(artifact)自定义 digestAlg?
		return digestContainerImageArtifact(normalized, client)
	}
	return digestLocalFileArtifact(normalized, digestAlg)
}

// Digest 返回制品(artifact)的摘要(digest)
func (a *DigestedArtifact) Digest() string {
	return a.digest
}

// Algorithm 返回制品(artifact)的算法
func (a *DigestedArtifact) Algorithm() string {
	return a.digestAlg
}

// DigestWithAlg 返回制品(artifact)的摘要(digest):算法(algorithm)
func (a *DigestedArtifact) DigestWithAlg() string {
	return fmt.Sprintf("%s:%s", a.digestAlg, a.digest)
}

func (a *DigestedArtifact) NameRef() name.Reference {
	return a.nameRef
}
