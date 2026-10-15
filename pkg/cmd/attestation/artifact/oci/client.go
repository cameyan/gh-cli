package oci

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/sigstore/sigstore-go/pkg/bundle"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

var ErrDenied = errors.New("提供的令牌被拒绝访问所请求的资源，请检查令牌的有效期和仓库访问权限。")
var ErrRegistryAuthz = errors.New("远程注册表授权失败，请与注册表进行身份验证后重试。")

type Client interface {
	GetImageDigest(imgName string) (*v1.Hash, name.Reference, error)
	GetAttestations(name name.Reference, digest string) ([]*api.Attestation, error)
}

func checkForUnauthorizedOrDeniedErr(err transport.Error) error {
	for _, diagnostic := range err.Errors {
		switch diagnostic.Code {
		case transport.UnauthorizedErrorCode:
			return ErrRegistryAuthz
		case transport.DeniedErrorCode:
			return ErrDenied
		}
	}
	return nil
}

type LiveClient struct {
	parseReference func(string, ...name.Option) (name.Reference, error)
	get            func(name.Reference, ...remote.Option) (*remote.Descriptor, error)
}

func (c LiveClient) ParseReference(ref string) (name.Reference, error) {
	return c.parseReference(ref)
}

// where name is formed like ghcr.io/github/my-image-repo
func (c LiveClient) GetImageDigest(imgName string) (*v1.Hash, name.Reference, error) {
	name, err := c.parseReference(imgName)
	if err != nil {
		return nil, nil, fmt.Errorf("创建镜像标签失败: %v", err)
	}
	// The user must already be authenticated with the container registry
	// The authn.DefaultKeychain argument indicates that Get should checks the
	// user's configuration for the registry credentials
	desc, err := c.get(name, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		if transportErr, ok := errors.AsType[*transport.Error](err); ok {
			if accessErr := checkForUnauthorizedOrDeniedErr(*transportErr); accessErr != nil {
				return nil, nil, accessErr
			}
		}
		return nil, nil, fmt.Errorf("无法获取远程镜像文件: %v", err)
	}

	return &desc.Digest, name, nil
}

func (c LiveClient) GetAttestations(ref name.Reference, digest string) ([]*api.Attestation, error) {
	attestations := make([]*api.Attestation, 0)

	transportOpts := []remote.Option{remote.WithAuthFromKeychain(authn.DefaultKeychain)}
	referrers, err := remote.Referrers(ref.Context().Digest(digest), transportOpts...)
	if err != nil {
		return attestations, fmt.Errorf("获取推荐人信息失败: %w", err)
	}
	refManifest, err := referrers.IndexManifest()
	if err != nil {
		return attestations, fmt.Errorf("获取推荐者清单失败: %w", err)
	}

	for _, refDesc := range refManifest.Manifests {
		if !strings.HasPrefix(refDesc.ArtifactType, "application/vnd.dev.sigstore.bundle") {
			continue
		}

		refImg, err := remote.Image(ref.Context().Digest(refDesc.Digest.String()), remote.WithAuthFromKeychain(authn.DefaultKeychain))
		if err != nil {
			return attestations, fmt.Errorf("获取推荐人图片失败: %w", err)
		}
		layers, err := refImg.Layers()
		if err != nil {
			return attestations, fmt.Errorf("获取推荐人图片失败: %w", err)
		}

		if len(layers) > 0 {
			layer0, err := layers[0].Uncompressed()
			if err != nil {
				return attestations, fmt.Errorf("获取推荐人图片失败: %w", err)
			}
			defer layer0.Close()

			bundleBytes, err := io.ReadAll(layer0)

			if err != nil {
				return attestations, fmt.Errorf("获取推荐人图片失败: %w", err)
			}

			b := &bundle.Bundle{}
			err = b.UnmarshalJSON(bundleBytes)

			if err != nil {
				return attestations, fmt.Errorf("解压 bundle 失败: %w", err)
			}

			a := api.Attestation{Bundle: b}
			attestations = append(attestations, &a)
		} else {
			return attestations, fmt.Errorf("获取推荐人图片失败: 未找到任何图层")
		}
	}
	return attestations, nil
}

// Unlike other parts of this command set, we cannot pass a custom HTTP client
// to the go-containerregistry library. This means we have limited visibility
// into the HTTP requests being made to container registries.
func NewLiveClient() *LiveClient {
	return &LiveClient{
		parseReference: name.ParseReference,
		get:            remote.Get,
	}
}
