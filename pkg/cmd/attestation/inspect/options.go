package inspect

import (
	"path/filepath"

	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
	"github.com/cli/cli/v2/pkg/cmdutil"
)

// Options 捕获 inspect 命令的选项
type Options struct {
	ArtifactPath     string
	BundlePath       string
	DigestAlgorithm  string
	Logger           *io.Handler
	OCIClient        oci.Client
	SigstoreVerifier verification.SigstoreVerifier
	exporter         cmdutil.Exporter
	Hostname         string
	Tenant           string
}

// Clean 清理文件路径选项值
func (opts *Options) Clean() {
	opts.BundlePath = filepath.Clean(opts.BundlePath)
}
