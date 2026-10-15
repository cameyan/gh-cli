package verification

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
)

const SLSAPredicateV1 = "https://slsa.dev/provenance/v1"

var ErrUnrecognisedBundleExtension = errors.New("bundle 文件扩展名不支持，必须为 json 或 jsonl")
var ErrEmptyBundleFile = errors.New("提供的 bundle 文件为空")

// GetLocalAttestations 返回从本地 bundle 文件中读取的证明(attestation)切片。
func GetLocalAttestations(path string) ([]*api.Attestation, error) {
	var attestations []*api.Attestation
	var err error
	fileExt := filepath.Ext(path)
	if fileExt == ".json" {
		attestations, err = loadBundleFromJSONFile(path)
	} else if fileExt == ".jsonl" {
		attestations, err = loadBundlesFromJSONLinesFile(path)
	} else {
		return nil, ErrUnrecognisedBundleExtension
	}

	if err != nil {
		if _, ok := errors.AsType[*os.PathError](err); ok {
			return nil, fmt.Errorf("无法从文件路径 %s 加载内容: %w", path, err)
		} else if errors.Is(err, bundle.ErrValidation) {
			return nil, err
		}
		return nil, fmt.Errorf("bundle 内容无法解析: %w", err)
	}

	return attestations, nil
}

func loadBundleFromJSONFile(path string) ([]*api.Attestation, error) {
	b, err := bundle.LoadJSONFromPath(path)
	if err != nil {
		return nil, err
	}

	return []*api.Attestation{{Bundle: b}}, nil
}

func loadBundlesFromJSONLinesFile(path string) ([]*api.Attestation, error) {
	fileContent, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	attestations := []*api.Attestation{}

	decoder := json.NewDecoder(bytes.NewReader(fileContent))

	for decoder.More() {
		var b bundle.Bundle
		b.Bundle = new(protobundle.Bundle)
		if err := decoder.Decode(&b); err != nil {
			return nil, err
		}
		a := api.Attestation{Bundle: &b}
		attestations = append(attestations, &a)
	}

	if len(attestations) == 0 {
		return nil, ErrEmptyBundleFile
	}

	return attestations, nil
}

func GetOCIAttestations(client oci.Client, artifact artifact.DigestedArtifact) ([]*api.Attestation, error) {
	attestations, err := client.GetAttestations(artifact.NameRef(), artifact.DigestWithAlg())
	if err != nil {
		return nil, fmt.Errorf("获取 OCI 证明(attestation)失败: %w", err)
	}
	if len(attestations) == 0 {
		return nil, fmt.Errorf("在 OCI 镜像仓库(registry)中未找到证明(attestation)。请在不带 --bundle-from-oci 标志(flag)的情况下重试该命令，以检查 GitHub 上的证明(attestation)")
	}
	return attestations, nil
}
