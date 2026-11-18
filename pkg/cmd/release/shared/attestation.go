package shared

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/artifact"
	att_io "github.com/cli/cli/v2/pkg/cmd/attestation/io"
	"github.com/cli/cli/v2/pkg/cmd/attestation/test/data"
	"github.com/cli/cli/v2/pkg/cmd/attestation/verification"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"

	v1 "github.com/in-toto/attestation/go/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type Verifier interface {
	// VerifyAttestation 校验给定制品(artifact)的证明(attestation)
	VerifyAttestation(art *artifact.DigestedArtifact, att *api.Attestation) (*verification.AttestationProcessingResult, error)
}

type AttestationVerifier struct {
	AttClient          api.Client
	ExternalHttpClient *http.Client
	IO                 *iostreams.IOStreams
	TrustedRoot        string
}

func (v *AttestationVerifier) VerifyAttestation(art *artifact.DigestedArtifact, att *api.Attestation) (*verification.AttestationProcessingResult, error) {
	td, err := v.AttClient.GetTrustDomain()
	if err != nil {
		return nil, err
	}

	verifier, err := verification.NewLiveSigstoreVerifier(verification.SigstoreConfig{
		ExternalHttpClient: v.ExternalHttpClient,
		Logger:             att_io.NewHandler(v.IO),
		NoPublicGood:       true,
		TrustDomain:        td,
		TrustedRoot:        v.TrustedRoot,
	})
	if err != nil {
		return nil, err
	}

	policy := buildVerificationPolicy(*art, td)
	sigstoreVerified, err := verifier.Verify([]*api.Attestation{att}, policy)
	if err != nil {
		return nil, err
	}

	return sigstoreVerified[0], nil
}

func FilterAttestationsByTag(attestations []*api.Attestation, tagName string) ([]*api.Attestation, error) {
	var filtered []*api.Attestation
	for _, att := range attestations {
		statement := att.Bundle.Bundle.GetDsseEnvelope().Payload
		var statementData v1.Statement
		err := protojson.Unmarshal([]byte(statement), &statementData)
		if err != nil {
			return nil, fmt.Errorf("反序列化 statement 失败: %w", err)
		}
		tagValue := statementData.Predicate.GetFields()["tag"].GetStringValue()

		if tagValue == tagName {
			filtered = append(filtered, att)
		}
	}
	return filtered, nil
}

func FilterAttestationsByFileDigest(attestations []*api.Attestation, fileDigest string) ([]*api.Attestation, error) {
	var filtered []*api.Attestation
	for _, att := range attestations {
		statement := att.Bundle.Bundle.GetDsseEnvelope().Payload
		var statementData v1.Statement
		err := protojson.Unmarshal([]byte(statement), &statementData)

		if err != nil {
			return nil, fmt.Errorf("反序列化 statement 失败: %w", err)
		}
		subjects := statementData.Subject
		for _, subject := range subjects {
			digestMap := subject.GetDigest()
			alg := "sha256"

			digest := digestMap[alg]
			if digest == fileDigest {
				filtered = append(filtered, att)
			}
		}

	}
	return filtered, nil
}

// buildVerificationPolicy 为 GitHub 发布(release)构造校验策略
func buildVerificationPolicy(a artifact.DigestedArtifact, trustDomain string) verify.PolicyBuilder {
	// 如果未指定信任域(trust domain)，默认为 "dotcom"
	if trustDomain == "" {
		trustDomain = "dotcom"
	}
	// SAN 必须匹配 GitHub releases 域名。无签发者(issuer)扩展(匹配任意值)
	sanMatcher, _ := verify.NewSANMatcher("", fmt.Sprintf("^https://%s\\.releases\\.github\\.com$", trustDomain))
	issuerMatcher, _ := verify.NewIssuerMatcher("", ".*")
	certId, _ := verify.NewCertificateIdentity(sanMatcher, issuerMatcher, certificate.Extensions{})

	artifactDigestPolicyOption, _ := verification.BuildDigestPolicyOption(a)
	return verify.NewPolicy(artifactDigestPolicyOption, verify.WithCertificateIdentity(certId))
}

type MockVerifier struct {
	mockResult *verification.AttestationProcessingResult
}

func NewMockVerifier(mockResult *verification.AttestationProcessingResult) *MockVerifier {
	return &MockVerifier{mockResult: mockResult}
}

func (v *MockVerifier) VerifyAttestation(art *artifact.DigestedArtifact, att *api.Attestation) (*verification.AttestationProcessingResult, error) {
	return &verification.AttestationProcessingResult{
		Attestation: &api.Attestation{
			Bundle:    data.GitHubReleaseBundle(nil),
			BundleURL: "https://example.com",
		},
		VerificationResult: nil,
	}, nil
}
