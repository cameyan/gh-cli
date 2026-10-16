package issues

import (
	"bytes"
	"testing"

	"github.com/cli/cli/v2/pkg/cmd/search/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/search"
	"github.com/google/shlex"
	"github.com/stretchr/testify/assert"
)

func TestNewCmdIssues(t *testing.T) {
	var trueBool = true
	tests := []struct {
		name    string
		input   string
		output  shared.IssuesOptions
		wantErr bool
		errMsg  string
	}{
		{
			name:    "no arguments",
			input:   "",
			wantErr: true,
			errMsg:  "请指定搜索关键字或 flag",
		},
		{
			name:  "keyword arguments",
			input: "some search terms",
			output: shared.IssuesOptions{
				Query: search.Query{
					Keywords:   []string{"some", "search", "terms"},
					Kind:       "issues",
					Limit:      30,
					Qualifiers: search.Qualifiers{Type: "issue"},
				},
			},
		},
		{
			name:  "web flag",
			input: "--web",
			output: shared.IssuesOptions{
				Query: search.Query{
					Keywords:   []string{},
					Kind:       "issues",
					Limit:      30,
					Qualifiers: search.Qualifiers{Type: "issue"},
				},
				WebMode: true,
			},
		},
		{
			name:  "limit flag",
			input: "--limit 10",
			output: shared.IssuesOptions{
				Query: search.Query{
					Keywords:   []string{},
					Kind:       "issues",
					Limit:      10,
					Qualifiers: search.Qualifiers{Type: "issue"},
				},
			},
		},
		{
			name:    "invalid limit flag",
			input:   "--limit 1001",
			wantErr: true,
			errMsg:  "`--limit` 必须在 1 到 1000 之间",
		},
		{
			name:  "order flag",
			input: "--order asc",
			output: shared.IssuesOptions{
				Query: search.Query{
					Keywords:   []string{},
					Kind:       "issues",
					Limit:      30,
					Order:      "asc",
					Qualifiers: search.Qualifiers{Type: "issue"},
				},
			},
		},
		{
			name:    "invalid order flag",
			input:   "--order invalid",
			wantErr: true,
			errMsg:  "invalid argument \"invalid\" for \"--order\" flag: valid values are {asc|desc}",
		},
		{
			name:  "include-prs flag",
			input: "--include-prs",
			output: shared.IssuesOptions{
				Query: search.Query{
					Keywords:   []string{},
					Kind:       "issues",
					Limit:      30,
					Qualifiers: search.Qualifiers{Type: ""},
				},
			},
		},
		{
			name:  "app flag",
			input: "--app dependabot",
			output: shared.IssuesOptions{
				Query: search.Query{
					Keywords:   []string{},
					Kind:       "issues",
					Limit:      30,
					Qualifiers: search.Qualifiers{Type: "issue", Author: "app/dependabot"},
				},
			},
		},
		{
			name:    "invalid author and app flags",
			input:   "--author test --app dependabot",
			wantErr: true,
			errMsg:  "只能指定 `--author` 或 `--app` 之一",
		},
		{
			name: "qualifier flags",
			input: `
      --archived
      --assignee=assignee
      --author=author
      --closed=closed
      --commenter=commenter
      --created=created
      --match=title,body
      --language=language
      --locked
      --mentions=mentions
      --no-label
      --repo=owner/repo
      --updated=updated
      --visibility=public
      `,
			output: shared.IssuesOptions{
				Query: search.Query{
					Keywords: []string{},
					Kind:     "issues",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Archived:  &trueBool,
						Assignee:  "assignee",
						Author:    "author",
						Closed:    "closed",
						Commenter: "commenter",
						Created:   "created",
						In:        []string{"title", "body"},
						Is:        []string{"public", "locked"},
						Language:  "language",
						Mentions:  "mentions",
						No:        []string{"label"},
						Repo:      []string{"owner/repo"},
						Type:      "issue",
						Updated:   "updated",
					},
				},
			},
		},
		{
			name:  "search-type semantic flag",
			input: "test --search-type semantic",
			output: shared.IssuesOptions{
				Query: search.Query{
					Keywords:        []string{"test"},
					Kind:            "issues",
					Limit:           30,
					IssueSearchType: "semantic",
					Qualifiers:      search.Qualifiers{Type: "issue"},
				},
			},
		},
		{
			name:  "search-type hybrid flag",
			input: "test --search-type hybrid",
			output: shared.IssuesOptions{
				Query: search.Query{
					Keywords:        []string{"test"},
					Kind:            "issues",
					Limit:           30,
					IssueSearchType: "hybrid",
					Qualifiers:      search.Qualifiers{Type: "issue"},
				},
			},
		},
		{
			name:  "search-type lexical flag sends no search type",
			input: "test --search-type lexical",
			output: shared.IssuesOptions{
				Query: search.Query{
					Keywords:   []string{"test"},
					Kind:       "issues",
					Limit:      30,
					Qualifiers: search.Qualifiers{Type: "issue"},
				},
			},
		},
		{
			name:    "invalid search-type flag",
			input:   "test --search-type bogus",
			wantErr: true,
			errMsg:  "invalid argument \"bogus\" for \"--search-type\" flag: valid values are {lexical|semantic|hybrid}",
		},
		{
			name:    "search-type semantic with include-prs flag",
			input:   "test --search-type semantic --include-prs",
			wantErr: true,
			errMsg:  "semantic 搜索仅限于 issue，不能与 `--include-prs` 组合使用",
		},
		{
			name:    "search-type semantic with web flag",
			input:   "test --search-type semantic --web",
			wantErr: true,
			errMsg:  "`--web` 不支持与 semantic 搜索同时使用",
		},
		{
			name:    "search-type semantic with sort flag",
			input:   "test --search-type semantic --sort comments",
			wantErr: true,
			errMsg:  "`--sort` 和 `--order` 不支持与 semantic 搜索同时使用",
		},
		{
			name:    "search-type semantic with order flag",
			input:   "test --search-type semantic --order asc",
			wantErr: true,
			errMsg:  "`--sort` 和 `--order` 不支持与 semantic 搜索同时使用",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			f := &cmdutil.Factory{
				IOStreams: ios,
			}
			argv, err := shlex.Split(tt.input)
			assert.NoError(t, err)
			var gotOpts *shared.IssuesOptions
			cmd := NewCmdIssues(f, func(opts *shared.IssuesOptions) error {
				gotOpts = opts
				return nil
			})
			cmd.SetArgs(argv)
			cmd.SetIn(&bytes.Buffer{})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			_, err = cmd.ExecuteC()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.output.Query, gotOpts.Query)
			assert.Equal(t, tt.output.WebMode, gotOpts.WebMode)
		})
	}
}
