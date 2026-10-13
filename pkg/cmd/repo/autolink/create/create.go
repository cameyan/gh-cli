package create

import (
	"fmt"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/repo/autolink/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type createOptions struct {
	BaseRepo       func() (ghrepo.Interface, error)
	Browser        browser.Browser
	AutolinkClient AutolinkCreateClient
	IO             *iostreams.IOStreams
	Exporter       cmdutil.Exporter

	KeyPrefix   string
	URLTemplate string
	Numeric     bool
}

type AutolinkCreateClient interface {
	Create(repo ghrepo.Interface, request AutolinkCreateRequest) (*shared.Autolink, error)
}

func NewCmdCreate(f *cmdutil.Factory, runF func(*createOptions) error) *cobra.Command {
	opts := &createOptions{
		Browser: f.Browser,
		IO:      f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "create <keyPrefix> <urlTemplate>",
		Short: "创建新的自动链接引用",
		Long: heredoc.Docf(`
			为仓库创建新的自动链接引用。

			%[1]skeyPrefix%[1]s 参数指定前缀，当该前缀后追加特定字符时将生成链接。

			%[1]surlTemplate%[1]s 参数指定找到 keyPrefix 时生成的目标 URL，
			其中必须包含表示引用编号的 %[1]s<num>%[1]s 变量。

			默认情况下，自动链接为字母数字类型，可使用 %[1]s--numeric%[1]s 标志创建数字类型的自动链接。

			%[1]s<num>%[1]s 变量的行为取决于自动链接是字母数字类型还是数字类型:

			- 字母数字类型: 匹配 %[1]sA-Z%[1]s(不区分大小写)、%[1]s0-9%[1]s 和 %[1]s-%[1]s
			- 数字类型: 匹配 %[1]s0-9%[1]s

			如果模板中包含多个 %[1]s<num>%[1]s 实例，仅第一个会被替换。
		`, "`"),
		Example: heredoc.Doc(`
			# 为键前缀 "TICKET-" 创建指向 example.com 的字母数字类型自动链接。
			# 从 "TICKET-123abc" 生成 https://example.com/TICKET?query=123abc。
			$ gh repo autolink create TICKET- "https://example.com/TICKET?query=<num>"

			# 为键前缀 "STORY-" 创建指向 example.com 的数字类型自动链接。
			# 从 "STORY-123" 生成 https://example.com/STORY?id=123。
			$ gh repo autolink create STORY- "https://example.com/STORY?id=<num>" --numeric
		`),
		Args:    cmdutil.ExactArgs(2, "无法创建自动链接: 必须同时提供 keyPrefix 和 urlTemplate 参数"),
		Aliases: []string{"new"},
		RunE: func(c *cobra.Command, args []string) error {
			opts.BaseRepo = f.BaseRepo

			httpClient, err := f.HttpClient()
			if err != nil {
				return err
			}

			opts.AutolinkClient = &AutolinkCreator{HTTPClient: httpClient}
			opts.KeyPrefix = args[0]
			opts.URLTemplate = args[1]

			if runF != nil {
				return runF(opts)
			}

			return createRun(opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.Numeric, "numeric", "n", false, "将自动链接标记为数字类型")

	return cmd
}

func createRun(opts *createOptions) error {
	repo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	request := AutolinkCreateRequest{
		KeyPrefix:      opts.KeyPrefix,
		URLTemplate:    opts.URLTemplate,
		IsAlphanumeric: !opts.Numeric,
	}

	autolink, err := opts.AutolinkClient.Create(repo, request)
	if err != nil {
		return fmt.Errorf("创建自动链接时出错: %w", err)
	}

	cs := opts.IO.ColorScheme()
	fmt.Fprintf(opts.IO.Out,
		"%s 已在 %s 上创建仓库自动链接 %s\n",
		cs.SuccessIconWithColor(cs.Green),
		cs.Cyanf("%d", autolink.ID),
		cs.Bold(ghrepo.FullName(repo)))

	return nil
}
