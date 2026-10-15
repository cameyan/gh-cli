package download

import (
	"fmt"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact/oci"
	"github.com/cli/cli/v2/pkg/cmd/attestation/io"
)

const (
	minLimit = 1
	maxLimit = 1000
)

type Options struct {
	APIClient       api.Client
	ArtifactPath    string
	DigestAlgorithm string
	Logger          *io.Handler
	Limit           int
	Store           MetadataStore
	OCIClient       oci.Client
	Owner           string
	PredicateType   string
	Repo            string
	Hostname        string
}

func (opts *Options) AreFlagsValid() error {
	// 检查 limit 是否介于 1 和 1000 之间
	if opts.Limit < minLimit || opts.Limit > maxLimit {
		return fmt.Errorf("不允许使用 limit %d，必须介于 %d 和 %d 之间", opts.Limit, minLimit, maxLimit)
	}

	return nil
}
