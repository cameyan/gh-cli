package verify

import (
	"errors"
	"fmt"

	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
)

func getAttestations(o *Options, a artifact.DigestedArtifact) ([]*api.Attestation, string, error) {
	// 在该 if 代码块内从 GitHub API 获取证明(attestation)，
	// 因为谓词类型(predicate type)过滤是在 API 调用时完成的
	if o.FetchAttestationsFromGitHubAPI() {
		if o.APIClient == nil {
			errMsg := "✗ 未提供 APIClient"
			return nil, errMsg, errors.New(errMsg)
		}

		params := api.FetchParams{
			Digest:        a.DigestWithAlg(),
			Limit:         o.Limit,
			Owner:         o.Owner,
			PredicateType: o.PredicateType,
			Repo:          o.Repo,
			Initiator:     "user",
		}

		attestations, err := o.APIClient.GetByDigest(params)
		if err != nil {
			msg := "✗ 从 GitHub API 加载证明(attestation)失败"
			return nil, msg, err
		}
		pluralAttestation := text.Pluralize(len(attestations), "attestation")
		msg := fmt.Sprintf("已从 GitHub API 加载 %s", pluralAttestation)
		return attestations, msg, nil
	}

	// 从本地 bundle 或 OCI 镜像仓库(registry)获取证明(attestation)
	// 谓词类型(predicate type)过滤在获取证明(attestation)之后进行
	var attestations []*api.Attestation
	var err error
	var msg string
	if o.BundlePath != "" {
		attestations, err = verification.GetLocalAttestations(o.BundlePath)
		if err != nil {
			pluralAttestation := text.Pluralize(len(attestations), "attestation")
			msg = fmt.Sprintf("已从 %s 加载 %s", o.BundlePath, pluralAttestation)
		} else {
			msg = fmt.Sprintf("已从 %s 加载 %d 个证明(attestation)", o.BundlePath, len(attestations))
		}
	} else if o.UseBundleFromRegistry {
		attestations, err = verification.GetOCIAttestations(o.OCIClient, a)
		if err != nil {
			msg = "✗ 从 OCI 镜像仓库(registry)加载证明(attestation)失败"
		} else {
			pluralAttestation := text.Pluralize(len(attestations), "attestation")
			msg = fmt.Sprintf("已从 OCI 镜像仓库(registry)加载 %s", pluralAttestation)
		}
	}
	if err != nil {
		return nil, msg, err
	}

	filtered, err := api.FilterAttestations(o.PredicateType, attestations)
	if err != nil {
		return nil, err.Error(), err
	}
	return filtered, msg, nil
}

func verifyAttestations(art artifact.DigestedArtifact, att []*api.Attestation, sgVerifier verification.SigstoreVerifier, ec verification.EnforcementCriteria) ([]*verification.AttestationProcessingResult, string, error) {
	sgPolicy, err := buildSigstoreVerifyPolicy(ec, art)
	if err != nil {
		logMsg := "✗ 构建 Sigstore 验证策略(verification policy)失败"
		return nil, logMsg, err
	}

	sigstoreVerified, err := sgVerifier.Verify(att, sgPolicy)
	if err != nil {
		logMsg := "✗ Sigstore 验证失败"
		return nil, logMsg, err
	}

	// 验证扩展(extensions)
	certExtVerified, err := verification.VerifyCertExtensions(sigstoreVerified, ec)
	if err != nil {
		logMsg := "✗ 策略(policy)验证失败"
		return nil, logMsg, err
	}

	return certExtVerified, "", nil
}
