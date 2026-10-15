package list

import (
	"fmt"
	"net/http"
	"time"

	"github.com/cli/cli/v2/api"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ListOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)

	Exporter cmdutil.Exporter
	Detector fd.Detector

	LimitResults       int
	ExcludeDrafts      bool
	ExcludePreReleases bool
	Order              string
}

func NewCmdList(f *cmdutil.Factory, runF func(*ListOptions) error) *cobra.Command {
	opts := &ListOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "列出仓库中的发布(release)",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			if opts.LimitResults < 1 {
				return cmdutil.FlagErrorf("无效的 limit 值: %v", opts.LimitResults)
			}

			if runF != nil {
				return runF(opts)
			}
			return listRun(opts)
		},
	}

	cmd.Flags().IntVarP(&opts.LimitResults, "limit", "L", 30, "获取条目的最大数量")
	cmd.Flags().BoolVar(&opts.ExcludeDrafts, "exclude-drafts", false, "排除草稿(draft)发布")
	cmd.Flags().BoolVar(&opts.ExcludePreReleases, "exclude-pre-releases", false, "排除预发布(pre-release)")
	cmdutil.StringEnumFlag(cmd, &opts.Order, "order", "O", "desc", []string{"asc", "desc"}, "返回发布(release)的排序方向")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, releaseFields)

	return cmd
}

func listRun(opts *ListOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	// TODO: immutableReleaseFullSupport
	// 当所覆盖的 GHES 版本完全支持不可变发布(immutable releases)时
	// (大概在 3.18 版本 EOL 之后)，将不再需要该探测器。
	if opts.Detector == nil {
		cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, baseRepo.RepoHost())
	}

	releaseFeatures, err := opts.Detector.ReleaseFeatures()
	if err != nil {
		return err
	}

	releases, err := fetchReleases(httpClient, baseRepo, opts.LimitResults, opts.ExcludeDrafts, opts.ExcludePreReleases, opts.Order, releaseFeatures)
	if err != nil {
		return err
	}

	if len(releases) == 0 && opts.Exporter == nil {
		return cmdutil.NewNoResultsError("未找到发布(release)")
	}

	if err := opts.IO.StartPager(); err == nil {
		defer opts.IO.StopPager()
	} else {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器(pager)失败: %v\n", err)
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, releases)
	}

	table := tableprinter.New(opts.IO, tableprinter.WithHeader("标题", "类型", "标签名", "发布时间"))
	cs := opts.IO.ColorScheme()
	for _, rel := range releases {
		title := text.RemoveExcessiveWhitespace(rel.Name)
		if title == "" {
			title = rel.TagName
		}
		table.AddField(title)

		badge := ""
		var badgeColor func(string) string
		if rel.IsLatest {
			badge = "最新"
			badgeColor = cs.Green
		} else if rel.IsDraft {
			badge = "草稿"
			badgeColor = cs.Red
		} else if rel.IsPrerelease {
			badge = "预发布"
			badgeColor = cs.Yellow
		}
		table.AddField(badge, tableprinter.WithColor(badgeColor))

		table.AddField(rel.TagName, tableprinter.WithTruncate(nil))

		pubDate := rel.PublishedAt
		if rel.PublishedAt.IsZero() {
			pubDate = rel.CreatedAt
		}
		table.AddTimeField(time.Now(), pubDate, cs.Muted)
		table.EndRow()
	}
	err = table.Render()
	if err != nil {
		return err
	}

	return nil
}
