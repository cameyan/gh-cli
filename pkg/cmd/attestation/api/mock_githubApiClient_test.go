package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	cliAPI "github.com/cli/cli/v2/api"
	ghAPI "github.com/cli/go-gh/v2/pkg/api"
	"github.com/stretchr/testify/mock"
)

type mockAPIClient struct {
	OnRESTWithNext func(hostname, method, p string, body io.Reader, data any) (string, error)
	OnREST         func(hostname, method, p string, body io.Reader, data any) error
}

func (m mockAPIClient) RESTWithNext(hostname, method, p string, body io.Reader, data any) (string, error) {
	return m.OnRESTWithNext(hostname, method, p, body, data)
}

func (m mockAPIClient) REST(hostname, method, p string, body io.Reader, data any) error {
	return m.OnREST(hostname, method, p, body, data)
}

type mockDataGenerator struct {
	mock.Mock
	NumUserAttestations   int
	NumGitHubAttestations int
}

func (m *mockDataGenerator) OnRESTSuccess(hostname, method, p string, body io.Reader, data any) (string, error) {
	return m.OnRESTWithNextSuccessHelper(hostname, method, p, body, data, false)
}

func (m *mockDataGenerator) OnRESTSuccessWithNextPage(hostname, method, p string, body io.Reader, data any) (string, error) {
	// 如果路径中不包含 after，说明是第一次请求 mock 服务器
	// 因此返回第一页数据，并在响应中返回 link header
	if !strings.Contains(p, "after") {
		return m.OnRESTWithNextSuccessHelper(hostname, method, p, body, data, true)
	}

	// 如果路径中包含 after，说明是第二次请求 mock 服务器，将不再返回 link header
	return m.OnRESTWithNextSuccessHelper(hostname, method, p, body, data, false)
}

// 返回一个调用 OnRESTSuccessWithNextPage 的函数，但有一半的概率返回 500 错误。
func (m *mockDataGenerator) FlakyOnRESTSuccessWithNextPageHandler() func(hostname, method, p string, body io.Reader, data any) (string, error) {
	// 设置故障(flake)计数器
	m.On("FlakyOnRESTSuccessWithNextPage:error").Return()

	count := 0
	return func(hostname, method, p string, body io.Reader, data any) (string, error) {
		if count%2 == 0 {
			m.MethodCalled("FlakyOnRESTSuccessWithNextPage:error")

			count = count + 1
			return "", cliAPI.HTTPError{HTTPError: &ghAPI.HTTPError{StatusCode: 500}}
		} else {
			count = count + 1
			return m.OnRESTSuccessWithNextPage(hostname, method, p, body, data)
		}
	}
}

// 总是返回 500
func (m *mockDataGenerator) OnREST500ErrorHandler() func(hostname, method, p string, body io.Reader, data any) (string, error) {
	m.On("OnREST500Error").Return()
	return func(hostname, method, p string, body io.Reader, data any) (string, error) {
		m.MethodCalled("OnREST500Error")

		return "", cliAPI.HTTPError{HTTPError: &ghAPI.HTTPError{StatusCode: 500}}
	}
}

func (m *mockDataGenerator) OnRESTWithNextSuccessHelper(hostname, method, p string, body io.Reader, data any, hasNext bool) (string, error) {
	atts := make([]*Attestation, m.NumUserAttestations+m.NumGitHubAttestations)
	for j := 0; j < m.NumUserAttestations; j++ {
		att := makeTestAttestation()
		atts[j] = &att
	}
	for j := m.NumUserAttestations; j < m.NumUserAttestations+m.NumGitHubAttestations; j++ {
		att := makeTestReleaseAttestation()
		atts[j] = &att
	}
	resp := AttestationsResponse{
		Attestations: atts,
	}

	// // 将证明(attestation)转换为 JSON
	b, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}

	err = json.Unmarshal(b, &data)
	if err != nil {
		return "", err
	}

	if hasNext {
		// 返回一个包含下一页的 link header
		return fmt.Sprintf("<%s&after=2>; rel=\"next\"", p), nil
	}

	return "", nil
}

func (m *mockDataGenerator) OnRESTWithNextNoAttestations(hostname, method, p string, body io.Reader, data any) (string, error) {
	resp := AttestationsResponse{
		Attestations: make([]*Attestation, 0),
	}

	// // 将证明(attestation)转换为 JSON
	b, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}

	err = json.Unmarshal(b, &data)
	if err != nil {
		return "", err
	}

	return "", nil
}

func (m *mockDataGenerator) OnRESTWithNextError(hostname, method, p string, body io.Reader, data any) (string, error) {
	return "", errors.New("获取证明(attestation)失败")
}

type mockMetaGenerator struct {
	TrustDomain string
}

func (m mockMetaGenerator) OnREST(hostname, method, p string, body io.Reader, data any) error {
	var template = `
{
  "domains": {
    "artifact_attestations": {
      "trust_domain": "%s"
    }
  }
}
`
	var jsonString = fmt.Sprintf(template, m.TrustDomain)
	return json.Unmarshal([]byte(jsonString), &data)

}

func (m mockMetaGenerator) OnRESTError(hostname, method, p string, body io.Reader, data any) error {
	return errors.New("测试错误")
}
