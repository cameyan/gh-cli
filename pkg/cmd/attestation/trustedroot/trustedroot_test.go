package trustedroot

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/internal/gh"
	ghmock "github.com/cli/cli/v2/internal/gh/mock"
	"github.com/cli/cli/v2/pkg/cmd/attestation/api"
	"github.com/cli/cli/v2/pkg/cmd/attestation/test"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/cli/cli/v2/pkg/iostreams"
)

func TestNewTrustedRootCmd(t *testing.T) {
	testIO, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: testIO,
		Config: func() (gh.Config, error) {
			return &ghmock.ConfigMock{}, nil
		},
		HttpClient: func() (*http.Client, error) {
			reg := &httpmock.Registry{}
			client := &http.Client{}
			httpmock.ReplaceTripper(client, reg)
			return client, nil
		},
		ExternalHttpClient: func() (*http.Client, error) {
			return nil, nil
		},
	}

	testcases := []struct {
		name     string
		cli      string
		wantsErr bool
	}{
		{
			name:     "正常路径(happy path)",
			cli:      "",
			wantsErr: false,
		},
		{
			name:     "正常路径(happy path)",
			cli:      "--verify-only",
			wantsErr: false,
		},
		{
			name:     "自定义 TUF 正常路径(happy path)",
			cli:      "--tuf-url https://tuf-repo.github.com --tuf-root ../verification/embed/tuf-repo.github.com/root.json",
			wantsErr: false,
		},
		{
			name:     "缺少 tuf-root 标志(flag)",
			cli:      "--tuf-url https://tuf-repo.github.com",
			wantsErr: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := NewTrustedRootCmd(f, func(_ *Options) error {
				return nil
			})

			argv := []string{}
			if tc.cli != "" {
				argv = strings.Split(tc.cli, " ")
			}
			cmd.SetArgs(argv)
			cmd.SetIn(&bytes.Buffer{})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			_, err := cmd.ExecuteC()
			if tc.wantsErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestNewTrustedRootWithTenancy(t *testing.T) {
	testIO, _, _, _ := iostreams.Test()
	var testReg httpmock.Registry
	var metaResp = api.MetaResponse{
		Domains: api.Domain{
			ArtifactAttestations: api.ArtifactAttestations{
				TrustDomain: "foo",
			},
		},
	}
	testReg.Register(httpmock.REST(http.MethodGet, "meta"),
		httpmock.StatusJSONResponse(200, &metaResp))

	httpClientFunc := func() (*http.Client, error) {
		reg := &testReg
		client := &http.Client{}
		httpmock.ReplaceTripper(client, reg)
		return client, nil
	}

	cli := "--hostname foo-bar.ghe.com"

	t.Run("主机(host)未配置身份认证", func(t *testing.T) {
		f := &cmdutil.Factory{
			IOStreams: testIO,
			Config: func() (gh.Config, error) {
				return &ghmock.ConfigMock{
					AuthenticationFunc: func() gh.AuthConfig {
						return &stubAuthConfig{hasActiveToken: false}
					},
				}, nil
			},
			HttpClient: httpClientFunc,
			ExternalHttpClient: func() (*http.Client, error) {
				return nil, nil
			},
		}

		cmd := NewTrustedRootCmd(f, func(_ *Options) error {
			return nil
		})

		argv := strings.Split(cli, " ")
		cmd.SetArgs(argv)
		cmd.SetIn(&bytes.Buffer{})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		_, err := cmd.ExecuteC()

		assert.Error(t, err)
		assert.ErrorContains(t, err, "身份认证")
	})

	t.Run("主机(host)已配置身份认证", func(t *testing.T) {
		f := &cmdutil.Factory{
			IOStreams: testIO,
			Config: func() (gh.Config, error) {
				return &ghmock.ConfigMock{
					AuthenticationFunc: func() gh.AuthConfig {
						return &stubAuthConfig{hasActiveToken: true}
					},
				}, nil
			},
			HttpClient: httpClientFunc,
			ExternalHttpClient: func() (*http.Client, error) {
				return nil, nil
			},
		}

		cmd := NewTrustedRootCmd(f, func(_ *Options) error {
			return nil
		})

		argv := strings.Split(cli, " ")
		cmd.SetArgs(argv)
		cmd.SetIn(&bytes.Buffer{})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})

		_, err := cmd.ExecuteC()
		assert.NoError(t, err)
	})
}

var newTUFErrClient tufClientInstantiator = func(o *tuf.Options) (*tuf.Client, error) {
	return nil, fmt.Errorf("创建 TUF 客户端(client)失败")
}

func TestGetTrustedRoot(t *testing.T) {
	mirror := "https://tuf-repo.github.com"
	root := test.NormalizeRelativePath("../verification/embed/tuf-repo.github.com/root.json")

	opts := &Options{
		TufUrl:      mirror,
		TufRootPath: root,
	}

	reg := &httpmock.Registry{}
	client := &http.Client{}
	httpmock.ReplaceTripper(client, reg)

	t.Run("创建 TUF 根(root)失败", func(t *testing.T) {
		err := getTrustedRoot(newTUFErrClient, opts, client)
		require.Error(t, err)
		require.ErrorContains(t, err, "创建 TUF 客户端(client)失败")
	})

	t.Run("因找不到根(root)而失败", func(t *testing.T) {
		opts.TufRootPath = test.NormalizeRelativePath("./does/not/exist/root.json")
		err := getTrustedRoot(tuf.New, opts, client)
		require.Error(t, err)
		require.ErrorContains(t, err, "读取根文件(root file)")
	})

}

type stubAuthConfig struct {
	config.AuthConfig
	hasActiveToken bool
}

var _ gh.AuthConfig = (*stubAuthConfig)(nil)

func (c *stubAuthConfig) HasActiveToken(host string) bool {
	return c.hasActiveToken
}
