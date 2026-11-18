package list

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/google/shlex"
	"github.com/stretchr/testify/assert"
)

func TestNewCmdList(t *testing.T) {
	tests := []struct {
		name     string
		cli      string
		wants    ListOptions
		wantsErr string
	}{
		{
			name: "无参数",
			cli:  "",
			wants: ListOptions{
				Limit: 30,
			},
		},
		{
			name: "带 limit 参数",
			cli:  "-L 101",
			wants: ListOptions{
				Limit: 101,
			},
		},
		{
			name:     "参数过多",
			cli:      "123 456",
			wantsErr: "accepts at most 1 arg(s), received 2",
		},
		{
			name:     "无效的 limit 值",
			cli:      "-L 0",
			wantsErr: "无效的 limit 值: 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &cmdutil.Factory{}

			argv, err := shlex.Split(tt.cli)
			assert.NoError(t, err)

			var gotOpts *ListOptions
			cmd := NewCmdList(f, func(opts *ListOptions) error {
				gotOpts = opts
				return nil
			})
			cmd.SetArgs(argv)
			cmd.SetIn(&bytes.Buffer{})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			_, err = cmd.ExecuteC()
			if tt.wantsErr != "" {
				assert.EqualError(t, err, tt.wantsErr)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wants.Limit, gotOpts.Limit)
		})
	}
}

func TestListRun(t *testing.T) {
	tests := []struct {
		name    string
		opts    ListOptions
		isTTY   bool
		wantOut string
	}{
		{
			name: "未找到任何组织",
			opts: ListOptions{HttpClient: func() (*http.Client, error) {
				r := &httpmock.Registry{}

				r.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data": {"viewer": {"login": "octocat"}}}`))

				r.Register(
					httpmock.GraphQL(`query OrganizationList\b`),
					httpmock.StringResponse(`
						{ "data": { "user": {
							"organizations": { "nodes": [], "totalCount": 0 }
						} } }`,
					),
				)

				return &http.Client{Transport: r}, nil
			}},
			isTTY: true,
			wantOut: heredoc.Doc(`

@octocat 没有关联任何组织

`),
		},
		{
			name: "默认行为",
			opts: ListOptions{HttpClient: func() (*http.Client, error) {
				r := &httpmock.Registry{}

				r.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data": {"viewer": {"login": "octocat"}}}`))

				r.Register(
					httpmock.GraphQL(`query OrganizationList\b`),
					httpmock.StringResponse(`
						{ "data": { "user": {	"organizations": {
							"totalCount": 2,
							"nodes": [
							{
								"login": "github"
							},
							{
								"login": "cli"
							}
						]	}	} } }`,
					),
				)

				return &http.Client{Transport: r}, nil
			}},
			isTTY: true,
			wantOut: heredoc.Doc(`

正在显示 2 个组织（共 2 个）

github
cli
`),
		},
		{
			name: "带 limit 参数",
			opts: ListOptions{Limit: 1, HttpClient: func() (*http.Client, error) {
				r := &httpmock.Registry{}

				r.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data": {"viewer": {"login": "octocat"}}}`))

				r.Register(
					httpmock.GraphQL(`query OrganizationList\b`),
					httpmock.StringResponse(`
						{ "data": { "user": {	"organizations": {
							"totalCount": 2,
							"nodes": [
							{
								"login": "github"
							},
							{
								"login": "cli"
							}
						]	}	} } }`,
					),
				)

				return &http.Client{Transport: r}, nil
			}},
			isTTY: true,
			wantOut: heredoc.Doc(`

正在显示 1 个组织（共 2 个）

github
`),
		},
		{
			name: "非 TTY 输出",
			opts: ListOptions{HttpClient: func() (*http.Client, error) {
				r := &httpmock.Registry{}

				r.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data": {"viewer": {"login": "octocat"}}}`))

				r.Register(
					httpmock.GraphQL(`query OrganizationList\b`),
					httpmock.StringResponse(`
						{ "data": { "user": {	"organizations": {
							"totalCount": 2,
							"nodes": [
							{
								"login": "github"
							},
							{
								"login": "cli"
							}
						]	}	} } }`,
					),
				)
				return &http.Client{Transport: r}, nil
			}},
			isTTY: false,
			wantOut: heredoc.Doc(`
github
cli
`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ios, _, stdout, _ := iostreams.Test()

			ios.SetStdoutTTY(tt.isTTY)
			ios.SetStdinTTY(tt.isTTY)
			ios.SetStderrTTY(tt.isTTY)

			tt.opts.IO = ios
			tt.opts.Config = func() (gh.Config, error) {
				return config.NewMockConfig(), nil
			}

			err := listRun(&tt.opts)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantOut, stdout.String())
		})
	}
}
