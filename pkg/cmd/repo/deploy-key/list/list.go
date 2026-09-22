package list

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ListOptions struct {
	IO         *iostreams.IOStreams
	HTTPClient func() (*http.Client, error)
	BaseRepo   func() (ghrepo.Interface, error)
	Exporter   cmdutil.Exporter
}

var deployKeyFields = []string{
	"id",
	"key",
	"title",
	"createdAt",
	"readOnly",
}

func NewCmdList(f *cmdutil.Factory, runF func(*ListOptions) error) *cobra.Command {
	opts := &ListOptions{
		IO:         f.IOStreams,
		HTTPClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "列出 GitHub 仓库中的部署密钥(deploy key)",
		Aliases: []string{"ls"},
		Args:    cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo

			if runF != nil {
				return runF(opts)
			}
			return listRun(opts)
		},
	}
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, deployKeyFields)
	return cmd
}

func listRun(opts *ListOptions) error {
	apiClient, err := opts.HTTPClient()
	if err != nil {
		return err
	}

	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	deployKeys, err := repoKeys(apiClient, repo)
	if err != nil {
		return err
	}

	if len(deployKeys) == 0 {
		return cmdutil.NewNoResultsError(fmt.Sprintf("%s 中未找到部署密钥", ghrepo.FullName(repo)))
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, deployKeys)
	}

	t := tableprinter.New(opts.IO, tableprinter.WithHeader("ID", "标题", "类型", "密钥", "创建时间"))
	cs := opts.IO.ColorScheme()
	now := time.Now()

	for _, deployKey := range deployKeys {
		sshID := strconv.FormatInt(deployKey.ID, 10)
		t.AddField(sshID)
		t.AddField(deployKey.Title)
		sshType := "只读"
		if !deployKey.ReadOnly {
			sshType = "读写"
		}
		t.AddField(sshType)
		t.AddField(deployKey.Key, tableprinter.WithTruncate(truncateMiddle))
		t.AddTimeField(now, deployKey.CreatedAt, cs.Muted)
		t.EndRow()
	}

	return t.Render()
}

func truncateMiddle(maxWidth int, t string) string {
	if len(t) <= maxWidth {
		return t
	}

	ellipsis := "..."
	if maxWidth < len(ellipsis)+2 {
		return t[0:maxWidth]
	}

	halfWidth := (maxWidth - len(ellipsis)) / 2
	remainder := (maxWidth - len(ellipsis)) % 2
	return t[0:halfWidth+remainder] + ellipsis + t[len(t)-halfWidth:]
}
