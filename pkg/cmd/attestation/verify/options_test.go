package verify

import (
	"testing"

	"github.com/cli/cli/v2/pkg/cmd/attestation/test"

	"github.com/stretchr/testify/require"
)

var (
	publicGoodArtifactPath = test.NormalizeRelativePath("../test/data/sigstore-js-2.1.0.tgz")
	publicGoodBundlePath   = test.NormalizeRelativePath("../test/data/psigstore-js-2.1.0-bundle.json")
)

var baseOptions = Options{
	ArtifactPath:    publicGoodArtifactPath,
	BundlePath:      publicGoodBundlePath,
	DigestAlgorithm: "sha512",
	Limit:           1,
	Owner:           "sigstore",
	OIDCIssuer:      "some issuer",
}

func TestAreFlagsValid(t *testing.T) {
	t.Run("Repo 值无效", func(t *testing.T) {
		opts := baseOptions
		opts.Repo = "sigstoresigstore-js"

		err := opts.AreFlagsValid()
		require.Error(t, err)
		require.ErrorContains(t, err, "为 repo 提供的值无效")
	})

	t.Run("无效 limit == 0", func(t *testing.T) {
		opts := baseOptions
		opts.Limit = 0

		err := opts.AreFlagsValid()
		require.Error(t, err)
		require.ErrorContains(t, err, "不允许使用 limit 0，必须介于 1 和 1000 之间")
	})

	t.Run("无效 limit > 1000", func(t *testing.T) {
		opts := baseOptions
		opts.Limit = 1001

		err := opts.AreFlagsValid()
		require.Error(t, err)
		require.ErrorContains(t, err, "不允许使用 limit 1001，必须介于 1 和 1000 之间")
	})

	t.Run("UseBundleFromRegistry 为 true 且 ArtifactPath 不是 OCI 路径时返回错误", func(t *testing.T) {
		opts := baseOptions
		opts.BundlePath = ""
		opts.UseBundleFromRegistry = true

		err := opts.AreFlagsValid()
		require.Error(t, err)
		require.ErrorContains(t, err, "bundle-from-oci 标志(flag)只能用于 OCI 制品(artifact)路径")
	})

	t.Run("UseBundleFromRegistry 为 true 且 ArtifactPath 是 OCI 路径时不返回错误", func(t *testing.T) {
		opts := baseOptions
		opts.ArtifactPath = "oci://sigstore/sigstore-js:2.1.0"
		opts.BundlePath = ""
		opts.UseBundleFromRegistry = true

		err := opts.AreFlagsValid()
		require.NoError(t, err)
	})

	t.Run("UseBundleFromRegistry 为 true 且提供了 BundlePath 时返回错误", func(t *testing.T) {
		opts := baseOptions
		opts.ArtifactPath = "oci://sigstore/sigstore-js:2.1.0"
		opts.UseBundleFromRegistry = true

		err := opts.AreFlagsValid()
		require.Error(t, err)
		require.ErrorContains(t, err, "bundle-from-oci 标志(flag)不能与 bundle-path 标志(flag)一起使用")
	})
}
