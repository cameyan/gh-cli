package api

import (
	"fmt"

	"github.com/cli/cli/v2/pkg/cmd/attestation/test/data"
)

func makeTestReleaseAttestation() Attestation {
	return Attestation{
		Bundle:    data.GitHubReleaseBundle(nil),
		BundleURL: "https://example.com",
		Initiator: "github",
	}
}

func makeTestAttestation() Attestation {
	return Attestation{
		Bundle:    data.SigstoreBundle(nil),
		BundleURL: "https://example.com",
		Initiator: "user",
	}
}

type MockClient struct {
	OnGetByDigest    func(params FetchParams) ([]*Attestation, error)
	OnGetTrustDomain func() (string, error)
}

func (m MockClient) GetByDigest(params FetchParams) ([]*Attestation, error) {
	return m.OnGetByDigest(params)
}

func (m MockClient) GetTrustDomain() (string, error) {
	return m.OnGetTrustDomain()
}

func OnGetByDigestSuccess(params FetchParams) ([]*Attestation, error) {
	att1 := makeTestAttestation()
	att2 := makeTestAttestation()
	att3 := makeTestReleaseAttestation()
	attestations := []*Attestation{&att1, &att2}
	if params.PredicateType != "" {
		// "release" 是一个哨兵值(sentinel value)，返回所有发布证明(release attestation)(v0.1、v0.2 等)
		// 这模拟了 GitHub API 在服务端处理该值的行为
		if params.PredicateType == "release" {
			return []*Attestation{&att3}, nil
		}
		return FilterAttestations(params.PredicateType, attestations)
	}

	return attestations, nil
}

func OnGetByDigestFailure(params FetchParams) ([]*Attestation, error) {
	if params.Repo != "" {
		return nil, fmt.Errorf("从 %s 获取证明失败", params.Repo)
	}
	return nil, fmt.Errorf("从 %s 获取证明失败", params.Owner)
}

func NewTestClient() *MockClient {
	return &MockClient{
		OnGetByDigest: OnGetByDigestSuccess,
	}
}

func NewFailTestClient() *MockClient {
	return &MockClient{
		OnGetByDigest: OnGetByDigestFailure,
	}
}
