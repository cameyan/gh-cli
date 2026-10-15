package status

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/charmbracelet/lipgloss"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/internal/tableprinter"
	"github.com/cli/cli/v2/pkg/cmd/factory"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/set"
	ghAPI "github.com/cli/go-gh/v2/pkg/api"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

type hostConfig interface {
	DefaultHost() (string, string)
}

type StatusOptions struct {
	HttpClient   func() (*http.Client, error)
	HostConfig   hostConfig
	CachedClient func(*http.Client, time.Duration) *http.Client
	IO           *iostreams.IOStreams
	Org          string
	Exclude      []string
}

func NewCmdStatus(f *cmdutil.Factory, runF func(*StatusOptions) error) *cobra.Command {
	opts := &StatusOptions{
		CachedClient: func(c *http.Client, ttl time.Duration) *http.Client {
			return api.NewCachedHTTPClient(c, ttl)
		},
	}
	opts.HttpClient = f.HttpClient
	opts.IO = f.IOStreams
	cmd := &cobra.Command{
		Use:   "status",
		Short: "打印跨仓库的相关 issue、PR 和通知信息",
		Long: heredoc.Doc(`
			status 命令打印你在 GitHub 上所订阅的所有仓库中的工作信息，包括:

			- 被指派的 Issue
			- 被指派的 Pull Request
			- 审核请求
			- 提及
			- 仓库活动(新 issue/PR、评论)
		`),
		Example: heredoc.Doc(`
		    # 排除多个仓库
			$ gh status -e cli/cli -e cli/go-gh

			# 将结果限定在单个组织内
			$ gh status -o cli
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := f.Config()
			if err != nil {
				return err
			}

			opts.HostConfig = cfg.Authentication()

			if runF != nil {
				return runF(opts)
			}

			return statusRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Org, "org", "o", "", "报告某个组织内的状态")
	cmd.Flags().StringSliceVarP(&opts.Exclude, "exclude", "e", []string{}, "以 owner/name 格式指定的要排除的仓库列表(逗号分隔)")

	return cmd
}

type Notification struct {
	Reason  string
	Subject struct {
		Title            string
		LatestCommentURL string `json:"latest_comment_url"`
		URL              string
		Type             string
	}
	Repository struct {
		Owner struct {
			Login string
		}
		FullName string `json:"full_name"`
	}
	index int
}

type StatusItem struct {
	Repository string // owner/repo
	Identifier string // 例如 cli/cli#1234 或仅 1234
	preview    string // 例如某段被截断的内容...
	Reason     string // 仅在仓库活动中使用
	index      int
}

func (s StatusItem) Preview() string {
	return strings.ReplaceAll(strings.ReplaceAll(s.preview, "\r", ""), "\n", " ")
}

type IssueOrPR struct {
	Number int
	Title  string
}

type Event struct {
	Type string
	Org  struct {
		Login string
	}
	CreatedAt time.Time `json:"created_at"`
	Repo      struct {
		Name string // owner/repo
	}
	Payload struct {
		Action      string
		Issue       IssueOrPR
		PullRequest IssueOrPR `json:"pull_request"`
		Comment     struct {
			Body    string
			HTMLURL string `json:"html_url"`
		}
	}
}

type SearchResult struct {
	Type       string `json:"__typename"`
	UpdatedAt  time.Time
	Title      string
	Number     int
	Repository struct {
		NameWithOwner string
	}
}

type Results []SearchResult

func (rs Results) Len() int {
	return len(rs)
}

func (rs Results) Less(i, j int) bool {
	return rs[i].UpdatedAt.After(rs[j].UpdatedAt)
}

func (rs Results) Swap(i, j int) {
	rs[i], rs[j] = rs[j], rs[i]
}

type stringSet interface {
	Len() int
	Add(string)
	ToSlice() []string
}

type StatusGetter struct {
	Client         *http.Client
	cachedClient   func(*http.Client, time.Duration) *http.Client
	host           string
	Org            string
	Exclude        []string
	AssignedPRs    []StatusItem
	AssignedIssues []StatusItem
	Mentions       []StatusItem
	ReviewRequests []StatusItem
	RepoActivity   []StatusItem

	authErrors   stringSet
	authErrorsMu sync.Mutex

	currentUsername string
	usernameMu      sync.Mutex
}

func NewStatusGetter(client *http.Client, hostname string, opts *StatusOptions) *StatusGetter {
	return &StatusGetter{
		Client:       client,
		Org:          opts.Org,
		Exclude:      opts.Exclude,
		cachedClient: opts.CachedClient,
		host:         hostname,
	}
}

func (s *StatusGetter) hostname() string {
	return s.host
}

func (s *StatusGetter) CachedClient(ttl time.Duration) *http.Client {
	return s.cachedClient(s.Client, ttl)
}

func (s *StatusGetter) ShouldExclude(repo string) bool {
	return slices.Contains(s.Exclude, repo)
}

func (s *StatusGetter) CurrentUsername() (string, error) {
	s.usernameMu.Lock()
	defer s.usernameMu.Unlock()

	if s.currentUsername != "" {
		return s.currentUsername, nil
	}

	cachedClient := s.CachedClient(time.Hour * 48)
	cachingAPIClient := api.NewClientFromHTTP(cachedClient)
	currentUsername, err := api.CurrentLoginName(cachingAPIClient, s.hostname())
	if err != nil {
		return "", fmt.Errorf("获取当前用户名失败: %w", err)
	}

	s.currentUsername = currentUsername
	return currentUsername, nil
}

func (s *StatusGetter) ActualMention(commentURL safeurl.SafeURL) (string, error) {
	currentUsername, err := s.CurrentUsername()
	if err != nil {
		return "", err
	}

	// 长缓存周期，因为一条评论一旦被查询过，就再也不需要查询它了。
	cachedClient := s.CachedClient(time.Hour * 24 * 30)
	c := api.NewClientFromHTTP(cachedClient)
	resp := struct {
		Body string
	}{}
	if err := c.REST(s.hostname(), "GET", commentURL.String(), nil, &resp); err != nil {
		return "", err
	}

	if strings.Contains(resp.Body, "@"+currentUsername) {
		return resp.Body, nil
	}

	return "", nil
}

// 按端点拆分这些方法，因为并行化工作正是沿着这一边界进行的

// 填充 .Mentions
func (s *StatusGetter) LoadNotifications() error {
	perPage := 100
	c := api.NewClientFromHTTP(s.Client)

	fetchWorkers := 10
	ctx, abortFetching := context.WithCancel(context.Background())
	defer abortFetching()
	toFetch := make(chan Notification)
	fetched := make(chan StatusItem)

	wg := new(errgroup.Group)
	for range fetchWorkers {
		wg.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case n, ok := <-toFetch:
					if !ok {
						return nil
					}
					actual, err := s.ActualMention(safeurl.NewImmutableSafeURL(n.Subject.LatestCommentURL))

					if err != nil {
						var httpErr api.HTTPError
						httpStatusCode := -1

						if errors.As(err, &httpErr) {
							httpStatusCode = httpErr.StatusCode
						}

						switch httpStatusCode {
						case 403:
							s.addAuthError(httpErr.Message, factory.SSOURL())
						case 404:
							return nil
						default:
							abortFetching()
							return fmt.Errorf("无法获取评论: %w", err)
						}
					}

					if actual != "" {
						// 我很抱歉(此处逻辑较为混乱)
						split := strings.Split(n.Subject.URL, "/")
						fetched <- StatusItem{
							Repository: n.Repository.FullName,
							Identifier: fmt.Sprintf("%s#%s", n.Repository.FullName, split[len(split)-1]),
							preview:    actual,
							index:      n.index,
						}
					}
				}
			}
		})
	}

	doneCh := make(chan struct{})
	go func() {
		for item := range fetched {
			s.Mentions = append(s.Mentions, item)
		}
		close(doneCh)
	}()

	// 这种方式很糟糕，不得不获取这么多数据 :/ 但在我的测试中，这是唯一能真正获取
	// 到足够多提及(mention)的方式。我很希望能直接过滤提及，但通知 API 似乎做不到。
	// 我本想换成 GraphQL 版本，但据我所知，它目前不支持 PAT。
	nIndex := 0
	u, err := safeurl.JoinPath("notifications")
	if err != nil {
		return err
	}
	u.SetQuery("per_page", strconv.Itoa(perPage))
	u.SetQuery("participating", "true")
	u.SetQuery("all", "true")
	var p safeurl.SafeURL = u
	for range 3 {
		var resp []Notification
		next, err := c.RESTWithNext(s.hostname(), "GET", p.String(), nil, &resp)
		if err != nil {
			var httpErr api.HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != 404 {
				return fmt.Errorf("无法获取通知: %w", err)
			}
		}

		for _, n := range resp {
			if n.Reason != "mention" {
				continue
			}
			if s.Org != "" && n.Repository.Owner.Login != s.Org {
				continue
			}
			if s.ShouldExclude(n.Repository.FullName) {
				continue
			}
			n.index = nIndex
			nIndex++
			toFetch <- n
		}

		if next == "" || len(resp) < perPage {
			break
		}
		p = safeurl.NewImmutableSafeURL(next)
	}

	close(toFetch)
	err = wg.Wait()
	close(fetched)
	<-doneCh
	sort.Slice(s.Mentions, func(i, j int) bool {
		return s.Mentions[i].index < s.Mentions[j].index
	})
	return err
}

const searchQuery = `
fragment issue on Issue {
	__typename
	updatedAt
	title
	number
	repository {
		nameWithOwner
	}
}
fragment pr on PullRequest {
	__typename
	updatedAt
	title
	number
	repository {
		nameWithOwner
	}
}
query AssignedSearch($searchAssigns: String!, $searchReviews: String!, $limit: Int = 25) {
	assignments: search(first: $limit, type: ISSUE, query: $searchAssigns) {
		nodes {
			...issue
			...pr
		}
	}
	reviewRequested: search(first: $limit, type: ISSUE, query: $searchReviews) {
		nodes {
			...pr
		}
	}
}`

// 填充 .AssignedPRs, .AssignedIssues, .ReviewRequests
func (s *StatusGetter) LoadSearchResults() error {
	c := api.NewClientFromHTTP(s.Client)

	var searchAssigns strings.Builder
	searchAssigns.WriteString(`assignee:@me state:open archived:false`)
	var searchReviews strings.Builder
	searchReviews.WriteString(`review-requested:@me state:open archived:false`)
	if s.Org != "" {
		searchAssigns.WriteString(" org:" + s.Org)
		searchReviews.WriteString(" org:" + s.Org)
	}
	for _, repo := range s.Exclude {
		searchAssigns.WriteString(" -repo:" + repo)
		searchReviews.WriteString(" -repo:" + repo)
	}
	variables := map[string]any{
		"searchAssigns": searchAssigns.String(),
		"searchReviews": searchReviews.String(),
	}

	var resp struct {
		Assignments struct {
			Nodes []*SearchResult
		}
		ReviewRequested struct {
			Nodes []*SearchResult
		}
	}
	err := c.GraphQL(s.hostname(), searchQuery, variables, &resp)
	if err != nil {
		if gqlErrResponse, ok := errors.AsType[api.GraphQLError](err); ok {
			gqlErrors := make([]ghAPI.GraphQLErrorItem, 0, len(gqlErrResponse.Errors))
			// 排除所有 FORBIDDEN 错误，并尽可能展示可得的状态。
			for _, gqlErr := range gqlErrResponse.Errors {
				if gqlErr.Type == "FORBIDDEN" {
					s.addAuthError(gqlErr.Message, factory.SSOURL())
				} else {
					gqlErrors = append(gqlErrors, gqlErr)
				}
			}
			if len(gqlErrors) == 0 {
				err = nil
			} else {
				err = api.GraphQLError{
					GraphQLError: &ghAPI.GraphQLError{
						Errors: gqlErrors,
					},
				}
			}
		}
		if err != nil {
			return fmt.Errorf("无法搜索指派结果: %w", err)
		}
	}

	prs := []SearchResult{}
	issues := []SearchResult{}
	reviewRequested := []SearchResult{}

	for _, node := range resp.Assignments.Nodes {
		if node == nil {
			continue // 很可能是 FORBIDDEN 结果所致
		}
		switch node.Type {
		case "Issue":
			issues = append(issues, *node)
		case "PullRequest":
			prs = append(prs, *node)
		default:
			return fmt.Errorf("未知的 Assignments 类型: %q", node.Type)
		}
	}

	for _, node := range resp.ReviewRequested.Nodes {
		if node == nil {
			continue // 很可能是 FORBIDDEN 结果所致
		}
		reviewRequested = append(reviewRequested, *node)
	}

	sort.Sort(Results(issues))
	sort.Sort(Results(prs))
	sort.Sort(Results(reviewRequested))

	s.AssignedIssues = []StatusItem{}
	s.AssignedPRs = []StatusItem{}
	s.ReviewRequests = []StatusItem{}

	for _, i := range issues {
		s.AssignedIssues = append(s.AssignedIssues, StatusItem{
			Repository: i.Repository.NameWithOwner,
			Identifier: fmt.Sprintf("%s#%d", i.Repository.NameWithOwner, i.Number),
			preview:    i.Title,
		})
	}

	for _, pr := range prs {
		s.AssignedPRs = append(s.AssignedPRs, StatusItem{
			Repository: pr.Repository.NameWithOwner,
			Identifier: fmt.Sprintf("%s#%d", pr.Repository.NameWithOwner, pr.Number),
			preview:    pr.Title,
		})
	}

	for _, r := range reviewRequested {
		s.ReviewRequests = append(s.ReviewRequests, StatusItem{
			Repository: r.Repository.NameWithOwner,
			Identifier: fmt.Sprintf("%s#%d", r.Repository.NameWithOwner, r.Number),
			preview:    r.Title,
		})
	}

	return nil
}

// 填充 .RepoActivity
func (s *StatusGetter) LoadEvents() error {
	perPage := 100
	c := api.NewClientFromHTTP(s.Client)

	currentUsername, err := s.CurrentUsername()
	if err != nil {
		return err
	}

	var events []Event
	var resp []Event
	pages := 0
	u, err := safeurl.JoinPath("users", currentUsername, "received_events")
	if err != nil {
		return err
	}
	u.SetQuery("per_page", strconv.Itoa(perPage))
	var p safeurl.SafeURL = u
	for pages < 2 {
		next, err := c.RESTWithNext(s.hostname(), "GET", p.String(), nil, &resp)
		if err != nil {
			var httpErr api.HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != 404 {
				return fmt.Errorf("无法获取事件: %w", err)
			}
		}
		events = append(events, resp...)
		if next == "" || len(resp) < perPage {
			break
		}

		pages++
		p = safeurl.NewImmutableSafeURL(next)
	}

	s.RepoActivity = []StatusItem{}

	for _, e := range events {
		if s.Org != "" && e.Org.Login != s.Org {
			continue
		}
		if s.ShouldExclude(e.Repo.Name) {
			continue
		}
		si := StatusItem{}
		var number int
		switch e.Type {
		case "IssuesEvent":
			if e.Payload.Action != "opened" {
				continue
			}
			si.Reason = "新 issue"
			si.preview = e.Payload.Issue.Title
			number = e.Payload.Issue.Number
		case "PullRequestEvent":
			if e.Payload.Action != "opened" {
				continue
			}
			si.Reason = "新 PR"
			si.preview = e.Payload.PullRequest.Title
			number = e.Payload.PullRequest.Number
		case "PullRequestReviewCommentEvent":
			si.Reason = "评论于 " + e.Payload.PullRequest.Title
			si.preview = e.Payload.Comment.Body
			number = e.Payload.PullRequest.Number
		case "IssueCommentEvent":
			si.Reason = "评论于 " + e.Payload.Issue.Title
			si.preview = e.Payload.Comment.Body
			number = e.Payload.Issue.Number
		default:
			continue
		}
		si.Repository = e.Repo.Name
		si.Identifier = fmt.Sprintf("%s#%d", e.Repo.Name, number)
		s.RepoActivity = append(s.RepoActivity, si)
	}

	return nil
}

func (s *StatusGetter) addAuthError(msg, ssoURL string) {
	s.authErrorsMu.Lock()
	defer s.authErrorsMu.Unlock()

	if s.authErrors == nil {
		s.authErrors = set.NewStringSet()
	}

	if ssoURL != "" {
		s.authErrors.Add(fmt.Sprintf("%s\n请在浏览器中授权: %s", msg, ssoURL))
	} else {
		s.authErrors.Add(msg)
	}
}

func (s *StatusGetter) HasAuthErrors() bool {
	s.authErrorsMu.Lock()
	defer s.authErrorsMu.Unlock()

	return s.authErrors != nil && s.authErrors.Len() > 0
}

func statusRun(opts *StatusOptions) error {
	client, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("无法创建客户端: %w", err)
	}

	hostname, _ := opts.HostConfig.DefaultHost()

	sg := NewStatusGetter(client, hostname, opts)

	// TODO 将各部分拆分为独立的子命令

	g := new(errgroup.Group)
	g.Go(func() error {
		if err := sg.LoadNotifications(); err != nil {
			return fmt.Errorf("无法加载通知: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		if err := sg.LoadEvents(); err != nil {
			return fmt.Errorf("无法加载事件: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		if err := sg.LoadSearchResults(); err != nil {
			return fmt.Errorf("搜索失败: %w", err)
		}
		return nil
	})

	opts.IO.StartProgressIndicator()
	err = g.Wait()
	opts.IO.StopProgressIndicator()
	if err != nil {
		return err
	}

	cs := opts.IO.ColorScheme()
	out := opts.IO.Out
	fullWidth := opts.IO.TerminalWidth()
	halfWidth := (fullWidth / 2) - 2

	idStyle := cs.Cyan
	leftHalfStyle := lipgloss.NewStyle().Width(halfWidth).Padding(0).MarginRight(1).BorderRight(true).BorderStyle(lipgloss.NormalBorder())
	rightHalfStyle := lipgloss.NewStyle().Width(halfWidth).Padding(0)

	section := func(header string, items []StatusItem, width, rowLimit int) (string, error) {
		tableOut := &bytes.Buffer{}
		fmt.Fprintln(tableOut, cs.Bold(header))
		if len(items) == 0 {
			fmt.Fprintln(tableOut, "这里什么都没有 ^_^")
		} else {
			//nolint:staticcheck // SA1019: Headers distract from numerous nested tables.
			tp := tableprinter.NewWithWriter(tableOut, opts.IO.IsStdoutTTY(), width, cs, tableprinter.NoHeader)
			for i, si := range items {
				if i == rowLimit {
					break
				}
				tp.AddField(si.Identifier, tableprinter.WithColor(idStyle), tableprinter.WithTruncate(nil))
				if si.Reason != "" {
					tp.AddField(si.Reason)
				}
				tp.AddField(si.Preview())
				tp.EndRow()
			}

			err := tp.Render()
			if err != nil {
				return "", err
			}
		}

		return tableOut.String(), nil
	}

	mSection, err := section("提及", sg.Mentions, halfWidth, 5)
	if err != nil {
		return fmt.Errorf("渲染 '提及' 失败: %w", err)
	}
	mSection = rightHalfStyle.Render(mSection)

	rrSection, err := section("审核请求", sg.ReviewRequests, halfWidth, 5)
	if err != nil {
		return fmt.Errorf("渲染 '审核请求' 失败: %w", err)
	}
	rrSection = leftHalfStyle.Render(rrSection)

	prSection, err := section("被指派的 Pull Request", sg.AssignedPRs, halfWidth, 5)
	if err != nil {
		return fmt.Errorf("渲染 '被指派的 Pull Request' 失败: %w", err)
	}
	prSection = rightHalfStyle.Render(prSection)

	issueSection, err := section("被指派的 Issue", sg.AssignedIssues, halfWidth, 5)
	if err != nil {
		return fmt.Errorf("渲染 '被指派的 Issue' 失败: %w", err)
	}
	issueSection = leftHalfStyle.Render(issueSection)

	raSection, err := section("仓库活动", sg.RepoActivity, fullWidth, 10)
	if err != nil {
		return fmt.Errorf("渲染 '仓库活动' 失败: %w", err)
	}

	fmt.Fprintln(out, lipgloss.JoinHorizontal(lipgloss.Top, issueSection, prSection))
	fmt.Fprintln(out, lipgloss.JoinHorizontal(lipgloss.Top, rrSection, mSection))
	fmt.Fprintln(out, raSection)

	if sg.HasAuthErrors() {
		errs := sg.authErrors.ToSlice()
		sort.Strings(errs)
		for _, msg := range errs {
			fmt.Fprintln(out, cs.Mutedf("警告: %s", msg))
		}
	}

	return nil
}

