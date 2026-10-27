package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/gh/ghtelemetry"
	"github.com/cli/cli/v2/internal/ghinstance"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/factory"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/jsoncolor"
	"github.com/cli/go-gh/v2/pkg/jq"
	"github.com/cli/go-gh/v2/pkg/template"
	"github.com/spf13/cobra"
)

const (
	ttyIndent = "  "
)

type ApiOptions struct {
	AppVersion        string
	InvokingAgent     string
	BaseRepo          func() (ghrepo.Interface, error)
	Branch            func() (string, error)
	Config            func() (gh.Config, error)
	HttpClient        func(api.HTTPClientOptions) (*http.Client, error)
	IO                *iostreams.IOStreams
	TelemetryDisabler ghtelemetry.Disabler

	Hostname            string
	RequestMethod       string
	RequestMethodPassed bool
	RequestPath         string
	RequestInputFile    string
	MagicFields         []string
	RawFields           []string
	RequestHeaders      []string
	Previews            []string
	ShowResponseHeaders bool
	Paginate            bool
	Slurp               bool
	Silent              bool
	Template            string
	CacheTTL            time.Duration
	FilterOutput        string
	Verbose             bool

	AllowEscapeSequences bool
}

func NewCmdApi(f *cmdutil.Factory, runF func(*ApiOptions) error) *cobra.Command {
	opts := ApiOptions{
		AppVersion:        f.AppVersion,
		InvokingAgent:     f.InvokingAgent,
		BaseRepo:          f.BaseRepo,
		Branch:            f.Branch,
		Config:            f.Config,
		HttpClient:        api.NewHTTPClient,
		IO:                f.IOStreams,
		TelemetryDisabler: f.TelemetryDisabler,
	}

	cmd := &cobra.Command{
		Use:   "api <endpoint>",
		Short: "发起经过身份验证的 GitHub API 请求",
		Long: heredoc.Docf(`
			向 GitHub API 发起一个经过身份验证的 HTTP 请求并打印响应。

			端点参数应为 GitHub API v3 端点的路径，或 %[1]sgraphql%[1]s 以访问 GitHub API v4。

			占位符值 %[1]s{owner}%[1]s、%[1]s{repo}%[1]s 和 %[1]s{branch}%[1]s 在端点参数中将
			替换为当前目录的仓库或 %[1]sGH_REPO%[1]s 环境变量中指定的仓库的值。请注意，在某些 shell(例如 PowerShell)中，
			您可能需要将包含 %[1]s{...}%[1]s 的任何值用引号括起来，以防止 shell 对大括号赋予特殊含义。

			%[1]s-p/--preview%[1]s 标志用于启用预览功能，这些功能是带有特性标记的实验性 API 端点或行为。
			API 期望通过 %[1]sAccept%[1]s 头字段以格式 %[1]sapplication/vnd.github.<preview-name>-preview+json%[1]s 进行
			选项加入，而此命令通过 %[1]s--preview <preview-name>%[1]s 来简化这一过程。若要发送针对 corsair 和
			scarlet 预览的请求，您可以使用 %[1]s-p corsair,scarlet-witch%[1]s 或 %[1]s--preview corsair --preview scarlet-witch%[1]s。

			默认的 HTTP 请求方法是 %[1]sGET%[1]s 或 %[1]sPOST%[1]s，可使用 %[1]s--method%[1]s 覆盖该方法。

			以 %[1]skey=value%[1]s 的格式传入一个或多个 %[1]s-f/--raw-field%[1]s 值，可将静态字符串参数添加
			到请求负载中。若需添加非字符串或占位符决定的值，请参阅下方的 %[1]s-F/--field%[1]s。请注意，添加请求参
			数会自动将请求方法切换为 %[1]sPOST%[1]s。若希望将参数作为 %[1]sGET%[1]s 查询字符串发送，
			请使用 %[1]s--method GET%[1]s。

			%[1]s-F/--field%[1]s 标志根据值的格式具有神奇的类型转换功能:

			- 字面值 %[1]strue%[1]s、%[1]sfalse%[1]s、%[1]snull%[1]s 以及整数会被转换为相应的 JSON 类型
			- 占位符值 %[1]s{owner}%[1]s、%[1]s{repo}%[1]s 和 %[1]s{branch}%[1]s 将从当前目录的仓库中获取相应的值填充
			- 如果值以 %[1]s@%[1]s 开头，则该值的其余部分将被解释为从中读取值的文件名。传递 %[1]s-%[1]s 可从标准输入读取

			对于 GraphQL 请求，除了 %[1]squery%[1]s 和 %[1]soperationName%[1]s 之外的所有字段均被解释
			为 GraphQL 变量。

			要在请求负载中传递嵌套参数，请在声明字段时使用 %[1]skey[subkey]=value%[1]s 语法。要将嵌套值作为数组传递，
			请使用 %[1]skey[]=value1%[1]s、%[1]skey[]=value2%[1]s 语法声明多个字段。要传递空数组，请使用不带值的
			%[1]skey[]%[1]s。

			要传递预先构建的 JSON 或其他格式的负载，可以从由 %[1]s--input%[1]s 指定的文件中读取请求体。使用 %[1]s-%[1]s
			可从标准输入读取。以这种方式传递请求体时，通过字段标志指定的任何参数都会添加到端点 URL 的查询字符串中。

			在 %[1]s--paginate%[1]s 模式下，所有结果页将按顺序请求，直到没有更多结果页为止。对于 GraphQL 请求，这要
			求原始查询必须接受一个 %[1]s$endCursor: String%[1]s 变量，并且从集合中获取
			%[1]spageInfo{ hasNextPage, endCursor }%[1]s 字段集。每一页都是一个独立的 JSON 数组或对象。传递
			%[1]s--slurp%[1]s 参数可将所有 JSON 数组或对象页封装到一个外层 JSON 数组中。
		`, "`"),
		Example: heredoc.Doc(`
			# 列出当前仓库中的发布版本
			$ gh api repos/{owner}/{repo}/releases

			# 发布问题评论
			$ gh api repos/{owner}/{repo}/issues/123/comments -f body='Hi from CLI'

			# 从文件中读取嵌套参数并发布
			$ gh api gists -F 'files[myfile.txt][content]=@myfile.txt'

			# 将参数添加到 GET 请求中
			$ gh api -X GET search/issues -f q='repo:cli/cli is:open remote'

			# 使用 JSON 文件作为请求体
			$ gh api repos/{owner}/{repo}/rulesets --input file.json

			# 设置自定义 HTTP 头
			$ gh api -H 'Accept: application/vnd.github.v3.raw+json' ...

			# 加入 GitHub API 预览版
			$ gh api --preview baptiste,nebula ...

			# 仅打印响应中的特定字段
			$ gh api repos/{owner}/{repo}/issues --jq '.[].title'

			# 使用模板格式化输出
			$ gh api repos/{owner}/{repo}/issues --template \
			  '{{range .}}{{.title}} ({{.labels | pluck "name" | join ", " | color "yellow"}}){{"\n"}}{{end}}'

			# 更新深层嵌套数组中“environment”自定义属性的允许值
			$ gh api -X PATCH /orgs/{org}/properties/schema \
			   -F 'properties[][property_name]=environment' \
			   -F 'properties[][default_value]=production' \
			   -F 'properties[][allowed_values][]=staging' \
			   -F 'properties[][allowed_values][]=production'

			# 使用 GraphQL 列出发布版本
			$ gh api graphql -F owner='{owner}' -F name='{repo}' -f query='
			  query($name: String!, $owner: String!) {
			    repository(owner: $owner, name: $name) {
			      releases(last: 3) {
			        nodes { tagName }
			      }
			    }
			  }
			'

			# 列出用户的所有仓库
			$ gh api graphql --paginate -f query='
			  query($endCursor: String) {
			    viewer {
			      repositories(first: 100, after: $endCursor) {
			        nodes { nameWithOwner }
			        pageInfo {
			          hasNextPage
			          endCursor
			        }
			      }
			    }
			  }
			'

			# 获取当前用户的分支百分比
			$ gh api graphql --paginate --slurp -f query='
			  query($endCursor: String) {
			    viewer {
			      repositories(first: 100, after: $endCursor) {
			        nodes { isFork }
			        pageInfo {
			          hasNextPage
			          endCursor
			        }
			      }
			    }
			  }
			' | jq 'def count(e): reduce e as $_ (0;.+1);
			[.[].data.viewer.repositories.nodes[]] as $r | count(select($r[].isFork))/count($r[])'
		`),
		Annotations: map[string]string{
			"help:environment": heredoc.Docf(`
				GH_TOKEN、GITHUB_TOKEN(按优先级顺序): 用于 %[1]sgithub.com%[1]s API 请求的身份验证令牌。

				GH_ENTERPRISE_TOKEN、GITHUB_ENTERPRISE_TOKEN(按优先级顺序): 用于向 GitHub Enterprise
				发起 API 请求的身份验证令牌。

				GH_HOST: 向除 %[1]sgithub.com%[1]s 以外的 GitHub 主机提出请求。
			`, "`"),
		},
		Args: cobra.ExactArgs(1),
		PreRun: func(c *cobra.Command, args []string) {
			opts.BaseRepo = cmdutil.OverrideBaseRepoFunc(f.BaseRepo, "")
		},
		RunE: func(c *cobra.Command, args []string) error {
			opts.RequestPath = args[0]
			opts.RequestMethodPassed = c.Flags().Changed("method")

			if runtime.GOOS == "windows" && filepath.IsAbs(opts.RequestPath) {
				return fmt.Errorf(`无效的 API 端点: "%s"。您的 shell 可能正在将 URL 路径重写为文件系统路径。为避免此问题，请从端点参数中省略开头的斜杠。`, opts.RequestPath)
			}

			if c.Flags().Changed("hostname") {
				if err := ghinstance.HostnameValidator(opts.Hostname); err != nil {
					return cmdutil.FlagErrorf("`--hostname` 解析错误: %w", err)
				}
			}

			if opts.Paginate && !strings.EqualFold(opts.RequestMethod, "GET") && opts.RequestPath != "graphql" {
				return cmdutil.FlagErrorf("`--paginate` 选项不支持非 GET 请求")
			}

			if err := cmdutil.MutuallyExclusive(
				"`--input` 不支持 `--paginate` 选项",
				opts.Paginate,
				opts.RequestInputFile != "",
			); err != nil {
				return err
			}

			if opts.Slurp {
				if err := cmdutil.MutuallyExclusive(
					"`--slurp` 选项不支持与 `--jq` 或 `--template` 一起使用",
					opts.Slurp,
					opts.FilterOutput != "",
					opts.Template != "",
				); err != nil {
					return err
				}

				if !opts.Paginate {
					return cmdutil.FlagErrorf("`--paginate` 在传递 `--slurp` 时是必需的")
				}
			}

			if err := cmdutil.MutuallyExclusive(
				"只能使用 `--template`、`--jq`、`--silent` 或 `--verbose` 中的一个",
				opts.Verbose,
				opts.Silent,
				opts.FilterOutput != "",
				opts.Template != "",
			); err != nil {
				return err
			}

			if runF != nil {
				return runF(&opts)
			}
			return apiRun(&opts)
		},
	}

	cmd.Flags().StringVar(&opts.Hostname, "hostname", "", "请求的 GitHub 主机名(默认 \"github.com\")")
	cmd.Flags().StringVarP(&opts.RequestMethod, "method", "X", "GET", "请求的 HTTP 方法")
	cmd.Flags().StringArrayVarP(&opts.MagicFields, "field", "F", nil, "在 `key=value` 格式中添加类型化参数(使用 \"@<path>\" 或 \"@-\" 从文件标志输入读取值)")
	cmd.Flags().StringArrayVarP(&opts.RawFields, "raw-field", "f", nil, "添加一个格式为 `key=value` 的字符串参数")
	cmd.Flags().StringArrayVarP(&opts.RequestHeaders, "header", "H", nil, "以 `key:value` 格式添加 HTTP 请求头")
	cmd.Flags().StringSliceVarP(&opts.Previews, "preview", "p", nil, "订阅 GitHub API 预览版功能(名称应省略 '-preview' 部分)")
	cmd.Flags().BoolVarP(&opts.ShowResponseHeaders, "include", "i", false, "在输出中包含 HTTP 响应状态行和头部信息")
	cmd.Flags().BoolVar(&opts.Slurp, "slurp", false, "与 \"--paginate\" 一起使用，以返回所有页面的数组，数组中的元素可以是 JSON 数组或对象")
	cmd.Flags().BoolVar(&opts.Paginate, "paginate", false, "发起额外的 HTTP 请求以获取所有结果页面")
	cmd.Flags().StringVar(&opts.RequestInputFile, "input", "", "用作 HTTP 请求正文的 `file`(使用 \"-\" 从标准输入读取数据)")
	cmd.Flags().BoolVar(&opts.Silent, "silent", false, "不要打印响应体")
	cmd.Flags().StringVarP(&opts.Template, "template", "t", "", "使用 Go 模板格式化 JSON 输出。请参阅 \"gh help formatting\"")
	cmd.Flags().StringVarP(&opts.FilterOutput, "jq", "q", "", "使用 jq 语法从响应中选择值的查询语句")
	cmd.Flags().DurationVar(&opts.CacheTTL, "cache", 0, "缓存响应，例如: \"3600s\"、\"60m\"、\"1h\"")
	cmd.Flags().BoolVar(&opts.Verbose, "verbose", false, "在输出中包含完整的 HTTP 请求和响应")
	cmd.Flags().BoolVar(&opts.AllowEscapeSequences, "allow-escape-sequences", false, "允许打印终端转义序列")
	return cmd
}

func apiRun(opts *ApiOptions) error {
	params, err := parseFields(opts)
	if err != nil {
		return err
	}

	isGraphQL := opts.RequestPath == "graphql"
	requestPath, err := fillPlaceholders(opts.RequestPath, opts)
	if err != nil {
		return fmt.Errorf("无法展开路径中的占位符: %w", err)
	}
	method := opts.RequestMethod
	requestHeaders := opts.RequestHeaders
	var requestBody any
	if len(params) > 0 {
		requestBody = params
	}

	if !opts.RequestMethodPassed && (len(params) > 0 || opts.RequestInputFile != "") {
		method = "POST"
	}

	if !opts.Silent {
		if err := opts.IO.StartPager(); err == nil {
			defer opts.IO.StopPager()
		} else {
			fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
		}
	}

	// 响应内容经由 ContentOut 输出。此处保持透传状态:
	// JSON 由传输层进行净化，而 jq/template/jsoncolor 路径会生成
	// 我们自己的格式化输出，因此只有原始的非 JSON 响应体需要中和处理，
	// 该处理在其下方的拷贝处完成。
	opts.IO.SetContentSanitization(false)

	var bodyWriter io.Writer = opts.IO.ContentOut
	var headersWriter io.Writer = opts.IO.Out
	if opts.Silent {
		bodyWriter = io.Discard
	}
	if opts.Verbose {
		// 指定 verbose 标志时由 httpClient 处理输出。
		bodyWriter = io.Discard
		headersWriter = io.Discard
	}

	if opts.Paginate && !isGraphQL {
		requestPath = addPerPage(requestPath, 100, params)
	}

	// 类似于 `jq --slurp`，将所有页面的 JSON 数组或对象写入一个 JSON 数组中。
	if opts.Paginate && opts.Slurp {
		w := &jsonArrayWriter{
			Writer: bodyWriter,
			color:  opts.IO.ColorEnabled(),
		}
		defer w.Close()

		bodyWriter = w
	}

	if opts.RequestInputFile != "" {
		file, size, err := openUserFile(opts.RequestInputFile, opts.IO.In)
		if err != nil {
			return err
		}
		defer file.Close()
		requestPath = addQuery(requestPath, params)
		requestBody = file
		if size >= 0 {
			requestHeaders = append([]string{fmt.Sprintf("Content-Length: %d", size)}, requestHeaders...)
		}
	}

	if len(opts.Previews) > 0 {
		requestHeaders = append(requestHeaders, "Accept: "+previewNamesToMIMETypes(opts.Previews))
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	if opts.HttpClient == nil {
		opts.HttpClient = api.NewHTTPClient
	}
	log := opts.IO.ErrOut
	if opts.Verbose {
		log = opts.IO.Out
	}
	httpClient, err := opts.HttpClient(api.HTTPClientOptions{
		AppVersion:        opts.AppVersion,
		InvokingAgent:     opts.InvokingAgent,
		CacheTTL:          opts.CacheTTL,
		RefreshingConfig:  cfg.Authentication(),
		EnableCache:       opts.CacheTTL > 0,
		Log:               log,
		LogColorize:       opts.IO.ColorEnabled(),
		LogVerboseHTTP:    opts.Verbose,
		TelemetryDisabler: opts.TelemetryDisabler,
	})
	if err != nil {
		return err
	}

	host, _ := cfg.Authentication().DefaultHost()

	if opts.Hostname != "" {
		host = opts.Hostname
	}

	apiHost, _ := cfg.Authentication().APIHostForHost(host)

	tmpl := template.New(bodyWriter, opts.IO.TerminalWidth(), opts.IO.ColorEnabled())
	err = tmpl.Parse(opts.Template)
	if err != nil {
		return err
	}

	isFirstPage := true
	hasNextPage := true
	for hasNextPage {
		resp, err := httpRequest(httpClient, host, apiHost, method, requestPath, requestBody, requestHeaders)
		if err != nil {
			return err
		}

		if !isGraphQL {
			requestPath, hasNextPage = findNextPage(resp)
			requestBody = nil // 防止重复发送 GET 参数
		}

		// 通知可选的 jsonArrayWriter 开始新的一页。
		err = startPage(bodyWriter)
		if err != nil {
			return err
		}

		endCursor, err := processResponse(resp, opts, bodyWriter, headersWriter, tmpl, isFirstPage, !hasNextPage)
		if err != nil {
			return err
		}
		isFirstPage = false

		if !opts.Paginate {
			break
		}

		if isGraphQL {
			hasNextPage = endCursor != ""
			if hasNextPage {
				params["endCursor"] = endCursor
			}
		}

		if hasNextPage && opts.ShowResponseHeaders {
			fmt.Fprint(opts.IO.Out, "\n")
		}
	}

	return tmpl.Flush()
}

var jsonContentTypeRE = regexp.MustCompile(`[/+]json(;|$)`)

func processResponse(resp *http.Response, opts *ApiOptions, bodyWriter, headersWriter io.Writer, template *template.Template, isFirstPage, isLastPage bool) (endCursor string, err error) {
	if opts.ShowResponseHeaders {
		fmt.Fprintln(headersWriter, resp.Proto, resp.Status)
		printHeaders(headersWriter, resp.Header, opts.IO.ColorEnabled())
		fmt.Fprint(headersWriter, "\r\n")
	}

	if resp.StatusCode == 204 {
		return
	}
	var responseBody io.Reader = resp.Body
	defer resp.Body.Close()

	isJSON := jsonContentTypeRE.MatchString(resp.Header.Get("Content-Type"))

	var serverError string
	if isJSON && (opts.RequestPath == "graphql" || resp.StatusCode >= 400) {
		if !strings.EqualFold(opts.RequestMethod, "HEAD") {
			responseBody, serverError, err = parseErrorResponse(responseBody, resp.StatusCode)
			if err != nil {
				return
			}
		}
	}

	var bodyCopy *bytes.Buffer
	isGraphQLPaginate := isJSON && resp.StatusCode == 200 && opts.Paginate && opts.RequestPath == "graphql"
	if isGraphQLPaginate {
		bodyCopy = &bytes.Buffer{}
		responseBody = io.TeeReader(responseBody, bodyCopy)
	}

	if opts.FilterOutput != "" && serverError == "" {
		// TODO: 在分页调用间复用已解析的查询
		indent := ""
		if opts.IO.IsStdoutTTY() {
			indent = ttyIndent
		}
		err = jq.EvaluateFormatted(responseBody, bodyWriter, opts.FilterOutput, indent, opts.IO.ColorEnabled())
		if err != nil {
			return
		}
	} else if opts.Template != "" && serverError == "" {
		err = template.Execute(responseBody)
		if err != nil {
			return
		}
	} else if isJSON && opts.IO.ColorEnabled() {
		err = jsoncolor.Write(bodyWriter, responseBody, ttyIndent)
	} else {
		if isJSON && opts.Paginate && !opts.Slurp && !isGraphQLPaginate && !opts.ShowResponseHeaders {
			responseBody = &paginatedArrayReader{
				Reader:      responseBody,
				isFirstPage: isFirstPage,
				isLastPage:  isLastPage,
			}
		}
		// 原始非 JSON 响应体是传输层唯一不进行净化的响应。
		// 它是逐字节的忠实输出，因此发往终端的二进制数据以及
		// 携带转义序列的文本都会被拒绝；可通过选择退出标志绕过，
		// 或将输出流丢弃后原样输出。
		if !isJSON && !opts.AllowEscapeSequences && bodyWriter != io.Discard {
			err = iostreams.CopyGuardedContent(bodyWriter, responseBody, opts.IO.IsStdoutTTY())
			if binErr, ok := errors.AsType[iostreams.BinaryTerminalError](err); ok {
				err = fmt.Errorf("%w; 请将标准输出重定向或通过管道保存，或传入 --allow-escape-sequences 以强制输出", binErr)
			} else if errors.Is(err, iostreams.ErrEscapeSequence) {
				err = errors.New("响应中包含终端转义序列; 请传入 --allow-escape-sequences 以强制输出")
			}
		} else {
			_, err = io.Copy(bodyWriter, responseBody)
		}
	}
	if err != nil {
		return
	}

	if serverError == "" && resp.StatusCode > 299 {
		serverError = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	if serverError != "" {
		fmt.Fprintf(opts.IO.ErrOut, "gh: %s\n", serverError)
		if msg := api.ScopesSuggestion(resp); msg != "" {
			fmt.Fprintf(opts.IO.ErrOut, "gh: %s\n", msg)
		}
		if u := factory.SSOURL(); u != "" {
			fmt.Fprintf(opts.IO.ErrOut, "请在浏览器中授权: %s\n", u)
		}
		err = cmdutil.SilentError
		return
	}

	if isGraphQLPaginate {
		endCursor = findEndCursor(bodyCopy)
	}

	return
}

var placeholderRE = regexp.MustCompile(`(\:(owner|repo|branch)\b|\{[a-z]+\})`)

// fillPlaceholders 使用当前仓库的值替换占位符
func fillPlaceholders(value string, opts *ApiOptions) (string, error) {
	var err error
	return placeholderRE.ReplaceAllStringFunc(value, func(m string) string {
		var name string
		if m[0] == ':' {
			name = m[1:]
		} else {
			name = m[1 : len(m)-1]
		}

		switch name {
		case "owner":
			if baseRepo, e := opts.BaseRepo(); e == nil {
				return baseRepo.RepoOwner()
			} else {
				err = e
			}
		case "repo":
			if baseRepo, e := opts.BaseRepo(); e == nil {
				return baseRepo.RepoName()
			} else {
				err = e
			}
		case "branch":
			if os.Getenv("GH_REPO") != "" {
				err = errors.New("无法为 'branch' 占位符确定合适的值")
				return m
			}

			if branch, e := opts.Branch(); e == nil {
				return branch
			} else {
				err = e
			}
		}
		return m
	}), err
}

func printHeaders(w io.Writer, headers http.Header, colorize bool) {
	var names []string
	for name := range headers {
		if name == "Status" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var headerColor, headerColorReset string
	if colorize {
		headerColor = "\x1b[1;34m" // 亮蓝色
		headerColorReset = "\x1b[m"
	}
	for _, name := range names {
		fmt.Fprintf(w, "%s%s%s: %s\r\n", headerColor, name, headerColorReset, strings.Join(headers[name], ", "))
	}
}

func openUserFile(fn string, stdin io.ReadCloser) (io.ReadCloser, int64, error) {
	if fn == "-" {
		return stdin, -1, nil
	}

	r, err := os.Open(fn)
	if err != nil {
		return r, -1, err
	}

	s, err := os.Stat(fn)
	if err != nil {
		return r, -1, err
	}

	return r, s.Size(), nil
}

func parseErrorResponse(r io.Reader, statusCode int) (io.Reader, string, error) {
	bodyCopy := &bytes.Buffer{}
	b, err := io.ReadAll(io.TeeReader(r, bodyCopy))
	if err != nil {
		return r, "", err
	}

	var parsedBody struct {
		Message string
		Errors  json.RawMessage
	}
	err = json.Unmarshal(b, &parsedBody)
	if err != nil {
		return bodyCopy, "", err
	}

	if len(parsedBody.Errors) > 0 && parsedBody.Errors[0] == '"' {
		var stringError string
		if err := json.Unmarshal(parsedBody.Errors, &stringError); err != nil {
			return bodyCopy, "", err
		}
		if stringError != "" {
			if parsedBody.Message != "" {
				return bodyCopy, fmt.Sprintf("%s (%s)", stringError, parsedBody.Message), nil
			}
			return bodyCopy, stringError, nil
		}
	}

	if parsedBody.Message != "" {
		return bodyCopy, fmt.Sprintf("%s (HTTP %d)", parsedBody.Message, statusCode), nil
	}

	if len(parsedBody.Errors) == 0 || parsedBody.Errors[0] != '[' {
		return bodyCopy, "", nil
	}

	var errorObjects []json.RawMessage
	if err := json.Unmarshal(parsedBody.Errors, &errorObjects); err != nil {
		return bodyCopy, "", err
	}

	var objectError struct {
		Message string
	}
	var errors []string
	for _, rawErr := range errorObjects {
		if len(rawErr) == 0 {
			continue
		}
		if rawErr[0] == '{' {
			err := json.Unmarshal(rawErr, &objectError)
			if err != nil {
				return bodyCopy, "", err
			}
			errors = append(errors, objectError.Message)
		} else if rawErr[0] == '"' {
			var stringError string
			err := json.Unmarshal(rawErr, &stringError)
			if err != nil {
				return bodyCopy, "", err
			}
			errors = append(errors, stringError)
		}
	}

	if len(errors) > 0 {
		return bodyCopy, strings.Join(errors, "\n"), nil
	}

	return bodyCopy, "", nil
}

func previewNamesToMIMETypes(names []string) string {
	types := []string{fmt.Sprintf("application/vnd.github.%s-preview+json", names[0])}
	for _, p := range names[1:] {
		types = append(types, fmt.Sprintf("application/vnd.github.%s-preview", p))
	}
	return strings.Join(types, ", ")
}
