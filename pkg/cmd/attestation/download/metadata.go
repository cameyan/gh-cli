package download

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/cli/cli/v2/internal/safepaths"
	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
)

var ErrAttestationFileCreation = fmt.Errorf("将证明写入文件失败")

type MetadataStore interface {
	createMetadataFile(artifactDigest string, attestationsResp []*api.Attestation) (string, error)
}

type LiveStore struct {
	outputPath string
}

func (s *LiveStore) createJSONLinesFilePath(artifact string) string {
	path := s.createJSONLinesFileName(artifact)
	if s.outputPath != "" {
		return fmt.Sprintf("%s/%s", s.outputPath, path)
	}
	return path
}

func (s *LiveStore) createJSONLinesFileName(artifact string) string {
	if runtime.GOOS == "windows" {
		// 冒号在 Windows 上是特殊字符，不能用于文件名。
		// 将其替换为短横线以避免问题。
		artifact = strings.ReplaceAll(artifact, ":", "-")
	}

	return fmt.Sprintf("%s.jsonl", artifact)
}

func (s *LiveStore) createMetadataFile(artifactDigest string, attestationsResp []*api.Attestation) (string, error) {
	metadataFilePath := s.createJSONLinesFilePath(artifactDigest)
	outputPath := s.outputPath
	if outputPath == "" {
		outputPath = "."
	}

	root, err := safepaths.OpenRootDir(outputPath, 0o755)
	if err != nil {
		return "", errors.Join(ErrAttestationFileCreation, fmt.Errorf("打开输出目录失败: %v", err))
	}
	defer root.Close()

	f, err := root.Create(s.createJSONLinesFileName(artifactDigest), 0o666, 0o755, true)
	if err != nil {
		return "", errors.Join(ErrAttestationFileCreation, fmt.Errorf("创建文件失败: %v", err))
	}

	for _, resp := range attestationsResp {
		bundle := resp.Bundle
		attBytes, err := json.Marshal(bundle)
		if err != nil {
			if err = f.Close(); err != nil {
				return "", errors.Join(ErrAttestationFileCreation, fmt.Errorf("序列化 JSON 时关闭文件失败: %v", err))
			}
			return "", errors.Join(ErrAttestationFileCreation, fmt.Errorf("写入文件时将证明序列化为 JSON 失败: %v", err))
		}

		withNewline := fmt.Sprintf("%s\n", attBytes)
		_, err = f.Write([]byte(withNewline))
		if err != nil {
			if err = f.Close(); err != nil {
				return "", errors.Join(ErrAttestationFileCreation, fmt.Errorf("处理写入错误时关闭文件失败: %v", err))
			}

			return "", errors.Join(ErrAttestationFileCreation, fmt.Errorf("写入证明失败: %v", err))
		}
	}

	if err = f.Close(); err != nil {
		return "", errors.Join(ErrAttestationFileCreation, fmt.Errorf("写入证明后关闭文件失败: %v", err))
	}

	return metadataFilePath, nil
}

func NewLiveStore(outputPath string) *LiveStore {
	return &LiveStore{
		outputPath: outputPath,
	}
}
