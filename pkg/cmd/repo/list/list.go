package list

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/spf13/cobra"

	"github.com/cli/cli/v2/api"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
)

type ListOptions struct {
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	Exporter   cmdutil.Exporter
	Detector   fd.Detector

	Limit int
	Owner string

	Visibility  string
	Fork        bool
	Source      bool
	Language    string
	Topic       []string
	Archived    bool
	NonArchived bool

	Now func() time.Time
}

func NewCmdList(f *cmdutil.Factory, runF func(*ListOptions) error) *cobra.Command {
	opts := ListOptions{
		IO:         f.IOStreams,
		Config:     f.Config,
		HttpClient: f.HttpClient,
		Now:        time.Now,
	}

	var (
		flagPublic  bool
		flagPrivate bool
	)

	cmd := &cobra.Command{
		Use:   "list [<owner>]",
		Args:  cobra.MaximumNArgs(1),
		Short: "列出用户或组织拥有的仓库",
		Long: heredoc.Docf(`
			列出某个用户或组织拥有的仓库。

			请注意，列表将仅包含由所提供参数拥有的仓库，且 %[1]s--fork%[1]s 或 %[1]s--source%[1]s 选项不会跨越所有权边界。
			例如，在列出组织中的派生仓库时，输出将不包含个人用户拥有的仓库。
		`, "`"),
		Aliases: []string{"ls"},
		RunE: func(c *cobra.Command, args []string) error {
			if opts.Limit < 1 {
				return cmdutil.FlagErrorf("无效的 limit: %v", opts.Limit)
			}

			if err := cmdutil.MutuallyExclusive("`--public`、`--private` 或 `--visibility` 中只能指定一个", flagPublic, flagPrivate, opts.Visibility != ""); err != nil {
				return err
			}
			if opts.Source && opts.Fork {
				return cmdutil.FlagErrorf("`--source` 或 `--fork` 中只能指定一个")
			}
			if opts.Archived && opts.NonArchived {
				return cmdutil.FlagErrorf("`--archived` 或 `--no-archived` 中只能指定一个")
			}

			if flagPrivate {
				opts.Visibility = "private"
			} else if flagPublic {
				opts.Visibility = "public"
			}

			if len(args) > 0 {
				opts.Owner = args[0]
			}

			if runF != nil {
				return runF(&opts)
			}
			return listRun(&opts)
		},
	}

	cmd.Flags().IntVarP(&opts.Limit, "limit", "L", 30, "要列出的仓库数量上限")
	cmd.Flags().BoolVar(&opts.Source, "source", false, "仅显示非 fork 仓库")
	cmd.Flags().BoolVar(&opts.Fork, "fork", false, "仅显示 fork 仓库")
	cmd.Flags().StringVarP(&opts.Language, "language", "l", "", "按主要编码语言过滤")
	cmd.Flags().StringSliceVarP(&opts.Topic, "topic", "", nil, "按主题过滤")
	cmdutil.StringEnumFlag(cmd, &opts.Visibility, "visibility", "", "", []string{"public", "private", "internal"}, "按仓库可见性过滤")
	cmd.Flags().BoolVar(&opts.Archived, "archived", false, "仅显示已归档的仓库")
	cmd.Flags().BoolVar(&opts.NonArchived, "no-archived", false, "省略已归档的仓库")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, api.RepositoryFields)

	cmd.Flags().BoolVar(&flagPrivate, "private", false, "仅显示私有仓库")
	cmd.Flags().BoolVar(&flagPublic, "public", false, "仅显示公开仓库")
	_ = cmd.Flags().MarkDeprecated("public", "请改用 `--visibility=public`")
	_ = cmd.Flags().MarkDeprecated("private", "请改用 `--visibility=private`")

	return cmd
}

var defaultFields = []string{"nameWithOwner", "description", "isPrivate", "isFork", "isArchived", "createdAt", "pushedAt"}

func listRun(opts *ListOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	host, _ := cfg.Authentication().DefaultHost()

	if opts.Detector == nil {
		cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
		opts.Detector = fd.NewDetector(cachedClient, host)
	}
	features, err := opts.Detector.RepositoryFeatures()
	if err != nil {
		return err
	}

	fields := defaultFields
	// TODO repoFeaturesCleanup
	if features.VisibilityField {
		fields = append(defaultFields, "visibility")
	}

	filter := FilterOptions{
		Visibility:  opts.Visibility,
		Fork:        opts.Fork,
		Source:      opts.Source,
		Language:    opts.Language,
		Topic:       opts.Topic,
		Archived:    opts.Archived,
		NonArchived: opts.NonArchived,
		Fields:      fields,
	}
	if opts.Exporter != nil {
		filter.Fields = opts.Exporter.Fields()
	}

	listResult, err := listRepos(httpClient, host, opts.Limit, opts.Owner, filter)
	if err != nil {
		return err
	}

	if opts.Owner != "" && listResult.Owner == "" && !listResult.FromSearch {
		return fmt.Errorf("所有者句柄 %q 未被识别为 GitHub 用户或组织", opts.Owner)
	}

	if err := opts.IO.StartPager(); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器出错: %v\n", err)
	}
	defer opts.IO.StopPager()

	if opts.Exporter != nil {
		return opts.Exporter.Write(opts.IO, listResult.Repositories)
	}

	cs := opts.IO.ColorScheme()
	tp := tableprinter.New(opts.IO, tableprinter.WithHeader("名称", "描述", "信息", "更新时间"))

	totalMatchCount := len(listResult.Repositories)
	for _, repo := range listResult.Repositories {
		info := repoInfo(repo)
		infoColor := cs.Muted

		if repo.IsPrivate {
			infoColor = cs.Yellow
		}

		t := repo.PushedAt
		if repo.PushedAt == nil {
			t = &repo.CreatedAt
		}

		tp.AddField(repo.NameWithOwner, tableprinter.WithColor(cs.Bold))
		tp.AddField(text.RemoveExcessiveWhitespace(repo.Description))
		tp.AddField(info, tableprinter.WithColor(infoColor))
		tp.AddTimeField(opts.Now(), *t, cs.Muted)
		tp.EndRow()
	}

	if listResult.FromSearch && opts.Limit > 1000 {
		fmt.Fprintln(opts.IO.ErrOut, "警告: 此查询使用 Search API, 该 API 最多返回 1000 条结果")
	}
	if opts.IO.IsStdoutTTY() {
		hasFilters := filter.Visibility != "" || filter.Fork || filter.Source || filter.Language != "" || len(filter.Topic) > 0
		title := listHeader(listResult.Owner, totalMatchCount, listResult.TotalCount, hasFilters)
		fmt.Fprintf(opts.IO.Out, "\n%s\n\n", title)
	}

	if totalMatchCount > 0 {
		return tp.Render()
	}

	return nil
}

func listHeader(owner string, matchCount, totalMatchCount int, hasFilters bool) string {
	if totalMatchCount == 0 {
		if hasFilters {
			return "没有符合搜索条件的结果"
		} else if owner != "" {
			return "@" + owner + " 中没有任何仓库"
		}
		return "没有结果"
	}

	var matchStr string
	if hasFilters {
		matchStr = " 条符合您的搜索"
	}
	return fmt.Sprintf("显示 @%s 中 %d 个仓库(共 %d 个)%s", owner, matchCount, totalMatchCount, matchStr)
}

func repoInfo(r api.Repository) string {
	tags := []string{visibilityLabel(r)}

	if r.IsFork {
		tags = append(tags, "fork")
	}
	if r.IsArchived {
		tags = append(tags, "archived")
	}

	return strings.Join(tags, ", ")
}

func visibilityLabel(repo api.Repository) string {
	if repo.Visibility != "" {
		return strings.ToLower(repo.Visibility)
	} else if repo.IsPrivate {
		return "private"
	}
	return "public"
}
