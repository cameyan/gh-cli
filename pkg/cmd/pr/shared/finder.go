package shared

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cli/cli/v2/api"
	ghContext "github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	o "github.com/cli/cli/v2/pkg/option"
	"github.com/cli/cli/v2/pkg/set"
	"github.com/shurcooL/githubv4"
	"golang.org/x/sync/errgroup"
)

type PRFinder interface {
	Find(opts FindOptions) (*api.PullRequest, ghrepo.Interface, error)
}

type progressIndicator interface {
	StartProgressIndicator()
	StopProgressIndicator()
}

type GitConfigClient interface {
	ReadBranchConfig(ctx context.Context, branchName string) (git.BranchConfig, error)
	PushDefault(ctx context.Context) (git.PushDefault, error)
	RemotePushDefault(ctx context.Context) (string, error)
	PushRevision(ctx context.Context, branchName string) (git.RemoteTrackingRef, error)
}

type finder struct {
	baseRepoFn      func() (ghrepo.Interface, error)
	branchFn        func() (string, error)
	httpClient      func() (*http.Client, error)
	remotesFn       func() (ghContext.Remotes, error)
	gitConfigClient GitConfigClient
	progress        progressIndicator

	baseRefRepo ghrepo.Interface
	prNumber    int
	branchName  string
}

func NewFinder(factory *cmdutil.Factory) PRFinder {
	if finderForRunCommandStyleTests != nil {
		f := finderForRunCommandStyleTests
		finderForRunCommandStyleTests = &mockFinder{err: errors.New("必须使用 StubFinderForRunCommandStyleTests 来为 PR 查找提供 stub")}
		return f
	}

	return &finder{
		baseRepoFn:      factory.BaseRepo,
		branchFn:        factory.Branch,
		httpClient:      factory.HttpClient,
		gitConfigClient: factory.GitClient,
		remotesFn:       factory.Remotes,
		progress:        factory.IOStreams,
	}
}

var finderForRunCommandStyleTests PRFinder

// StubFinderForRunCommandStyleTests 是 NewMockFinder 的替代品，仅可在 runCommand 风格的测试中使用。
func StubFinderForRunCommandStyleTests(t *testing.T, selector string, pr *api.PullRequest, repo ghrepo.Interface) *mockFinder {
	// 创建一个新的 mock finder 并覆盖 "runCommandFinder" 变量，使对 NewFinder() 的调用
	// 返回该 mock。这是一种不好的模式，是旧式 runCommand 测试的产物，理想情况下应被替换。
	// 我们需要这样做的原因是: runCommand 风格的测试通过 NewCmd* 函数构造 cobra 命令，
	// 然后直接执行它们，除非在 factory 上注入测试替身，否则没有机会注入；
	// 而 finder 从不在 factory 上，因为只有 PR 命令需要它。
	finder := NewMockFinder(selector, pr, repo)
	finderForRunCommandStyleTests = finder

	// 确保测试结束时重置 "runCommandFinder" 变量，使各测试相互隔离
	// ——至少在顺序运行时如此。
	t.Cleanup(func() {
		finderForRunCommandStyleTests = nil
	})
	return finder
}

type FindOptions struct {
	// Selector 可以是带可选 `#` 前缀的编号、带可选 `<owner>:` 前缀的分支名，或 PR URL。
	Selector string
	// Fields 列出要为 PullRequest 获取的 GraphQL 字段。
	Fields []string
	// BaseBranch 是将按分支查找 PR 的范围限定到的基础分支(base branch)名。
	BaseBranch string
	// States 列出将按分支查找 PR 的范围限定到的可能 PR 状态。
	States []string

	DisableProgress bool

	Detector fd.Detector
}

func (f *finder) Find(opts FindOptions) (*api.PullRequest, ghrepo.Interface, error) {
	// 如果我们有 URL，就不需要 git 相关的东西
	if len(opts.Fields) == 0 {
		return nil, nil, errors.New("Find 错误: 未指定任何字段")
	}

	if repo, prNumber, _, err := ParseURL(opts.Selector); err == nil {
		f.prNumber = prNumber
		f.baseRefRepo = repo
	}

	if f.baseRefRepo == nil {
		repo, err := f.baseRepoFn()
		if err != nil {
			return nil, nil, err
		}
		f.baseRefRepo = repo
	}

	var prRefs PRFindRefs
	if opts.Selector == "" {
		// 要让这种情况生效，必须处于 git 仓库中
		currentBranchName, err := f.branchFn()
		if err != nil {
			return nil, nil, err
		}
		f.branchName = currentBranchName

		// 获取当前 branchName 的分支配置
		branchConfig, err := f.gitConfigClient.ReadBranchConfig(context.Background(), f.branchName)
		if err != nil {
			return nil, nil, err
		}

		// 判断该分支是否被配置为合并到特殊的 PR ref
		prHeadRE := regexp.MustCompile(`^refs/pull/(\d+)/head$`)
		if m := prHeadRE.FindStringSubmatch(branchConfig.MergeRef); m != nil {
			prNumber, _ := strconv.Atoi(m[1])
			f.prNumber = prNumber
		}

		// 从配置确定 PullRequestRefs
		if f.prNumber == 0 {
			prRefsResolver := NewPullRequestFindRefsResolver(
				// 已经请求过分支配置，因此缓存它
				CachedBranchConfigGitConfigClient{
					CachedBranchConfig: branchConfig,
					GitConfigClient:    f.gitConfigClient,
				},
				f.remotesFn,
			)
			prRefs, err = prRefsResolver.ResolvePullRequestRefs(f.baseRefRepo, opts.BaseBranch, f.branchName)
			if err != nil {
				return nil, nil, err
			}
		}
	} else if f.prNumber == 0 {
		// 你给了我一个 selector，但我没能从中找到 PR 编号(它不是 URL)

		// 尝试从 selector 获取 PR 编号
		prNumber, err := strconv.Atoi(strings.TrimPrefix(opts.Selector, "#"))
		// 若 opts.Selector 是有效数字，则假定它就是 PR 编号——除非指定了 opts.BaseBranch。
		// 这是 PR create 命令的特殊情况，该命令总是希望假定数字型 selector
		// 是分支名而非 PR 编号。
		if opts.BaseBranch == "" && err == nil {
			f.prNumber = prNumber
		} else {
			f.branchName = opts.Selector

			qualifiedHeadRef, err := ParseQualifiedHeadRef(f.branchName)
			if err != nil {
				return nil, nil, err
			}

			prRefs = PRFindRefs{
				qualifiedHeadRef: qualifiedHeadRef,
				baseRepo:         f.baseRefRepo,
				baseBranchName:   o.SomeIfNonZero(opts.BaseBranch),
			}
		}
	}

	// 设置 HTTP client
	httpClient, err := f.httpClient()
	if err != nil {
		return nil, nil, err
	}

	// TODO: 将 PR finder 与 IO 解耦
	// TODO(josebalius): 我们应该在这里做保护吗?
	if !opts.DisableProgress && f.progress != nil {
		f.progress.StartProgressIndicator()
		defer f.progress.StopProgressIndicator()
	}

	fields := set.NewStringSet()
	fields.AddValues(opts.Fields)
	fields.AddValues([]string{"id", "number"}) // 供下方的附加预加载查询使用

	if fields.Contains("isInMergeQueue") || fields.Contains("isMergeQueueEnabled") {
		if opts.Detector == nil {
			cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
			opts.Detector = fd.NewDetector(cachedClient, f.baseRefRepo.RepoHost())
		}
		prFeatures, err := opts.Detector.PullRequestFeatures()
		if err != nil {
			return nil, nil, err
		}
		// TODO mergeQueueCleanup
		if !prFeatures.MergeQueue {
			fields.Remove("isInMergeQueue")
			fields.Remove("isMergeQueueEnabled")
		}
	}

	var getProjectItems bool
	if fields.Contains("projectItems") {
		getProjectItems = true
		fields.Remove("projectItems")
	}

	// TODO projectsV1Deprecation
	// 移除此代码块
	// 移除时，记得从 pr/view.go 的默认字段列表中移除 `projectCards`
	if fields.Contains("projectCards") {
		if opts.Detector == nil {
			cachedClient := api.NewCachedHTTPClient(httpClient, time.Hour*24)
			opts.Detector = fd.NewDetector(cachedClient, f.baseRefRepo.RepoHost())
		}

		if opts.Detector.ProjectsV1() == gh.ProjectsV1Unsupported {
			fields.Remove("projectCards")
		}
	}

	var pr *api.PullRequest
	if f.prNumber > 0 {
		pr, err = findByNumber(httpClient, f.baseRefRepo, f.prNumber, fields.ToSlice())
		if err != nil {
			return pr, f.baseRefRepo, err
		}
	} else if prRefs.BaseRepo() != nil && f.branchName != "" {
		// 没有 PR 编号，但我们有基础仓库和分支名。
		pr, err = findForRefs(httpClient, prRefs, opts.States, fields.ToSlice())
		if err != nil {
			return pr, f.baseRefRepo, err
		}
	} else {
		// 如果我们既没有 PR 编号，也没有基础仓库和分支名，那就无能为力了
		return nil, f.baseRefRepo, &NotFoundError{fmt.Errorf("未找到任何拉取请求(PR)")}
	}

	g, _ := errgroup.WithContext(context.Background())
	if fields.Contains("reviews") {
		g.Go(func() error {
			return preloadPrReviews(httpClient, f.baseRefRepo, pr)
		})
	}
	if fields.Contains("comments") {
		g.Go(func() error {
			return preloadPrComments(httpClient, f.baseRefRepo, pr)
		})
	}
	if fields.Contains("closingIssuesReferences") {
		g.Go(func() error {
			return preloadPrClosingIssuesReferences(httpClient, f.baseRefRepo, pr)
		})
	}
	if fields.Contains("statusCheckRollup") {
		g.Go(func() error {
			return preloadPrChecks(httpClient, f.baseRefRepo, pr)
		})
	}
	if getProjectItems {
		g.Go(func() error {
			apiClient := api.NewClientFromHTTP(httpClient)
			err := api.ProjectsV2ItemsForPullRequest(apiClient, f.baseRefRepo, pr)
			if err != nil && !api.ProjectsV2IgnorableError(err) {
				return err
			}
			return nil
		})
	}

	return pr, f.baseRefRepo, g.Wait()
}

var pullURLRE = regexp.MustCompile(`^/([^/]+)/([^/]+)/pull/(\d+)(.*$)`)

// ParseURL 解析拉取请求(PR) URL，返回仓库、PR 编号以及任何尾部的路径组成部分。
// 若没有错误，返回的 repo 不为 nil，且其主机名非空。
func ParseURL(prURL string) (ghrepo.Interface, int, string, error) {
	if prURL == "" {
		return nil, 0, "", fmt.Errorf("无效的 URL: %q", prURL)
	}

	u, err := url.Parse(prURL)
	if err != nil {
		return nil, 0, "", err
	}

	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, 0, "", fmt.Errorf("无效的协议(scheme): %s", u.Scheme)
	}

	m := pullURLRE.FindStringSubmatch(u.Path)
	if m == nil {
		return nil, 0, "", fmt.Errorf("不是拉取请求(PR) URL: %s", prURL)
	}

	repo := ghrepo.NewWithHost(m[1], m[2], u.Hostname())
	prNumber, _ := strconv.Atoi(m[3])
	tail := m[4]
	return repo, prNumber, tail, nil
}

var fullReferenceRE = regexp.MustCompile(`^(?:([^/]+)/([^/]+))#(\d+)$`)

// ParseFullReference 解析形如 "owner/repo#number" 的 issue/拉取请求(PR)短引用，
// 其中 owner、repo 和 number 均为必填。
func ParseFullReference(s string) (ghrepo.Interface, int, error) {
	if s == "" {
		return nil, 0, errors.New("引用为空")
	}

	m := fullReferenceRE.FindStringSubmatch(s)
	if m == nil {
		return nil, 0, fmt.Errorf("无效的引用: %q", s)
	}

	number, err := strconv.Atoi(m[3])
	if err != nil {
		return nil, 0, fmt.Errorf("无效的引用: %q; %w", s, err)
	}

	owner := m[1]
	repo := m[2]

	return ghrepo.New(owner, repo), number, nil
}

func findByNumber(httpClient *http.Client, repo ghrepo.Interface, number int, fields []string) (*api.PullRequest, error) {
	type response struct {
		Repository struct {
			PullRequest api.PullRequest
		}
	}

	query := fmt.Sprintf(`
	query PullRequestByNumber($owner: String!, $repo: String!, $pr_number: Int!) {
		repository(owner: $owner, name: $repo) {
			pullRequest(number: $pr_number) {%s}
		}
	}`, api.PullRequestGraphQL(fields))

	variables := map[string]any{
		"owner":     repo.RepoOwner(),
		"repo":      repo.RepoName(),
		"pr_number": number,
	}

	var resp response
	client := api.NewClientFromHTTP(httpClient)
	err := client.GraphQL(repo.RepoHost(), query, variables, &resp)
	if err != nil {
		return nil, err
	}

	return &resp.Repository.PullRequest, nil
}

func findForRefs(httpClient *http.Client, prRefs PRFindRefs, stateFilters, fields []string) (*api.PullRequest, error) {
	type response struct {
		Repository struct {
			PullRequests struct {
				Nodes []api.PullRequest
			}
			DefaultBranchRef struct {
				Name string
			}
		}
	}

	fieldSet := set.NewStringSet()
	fieldSet.AddValues(fields)
	// 这些字段是下方过滤所必需的
	fieldSet.AddValues([]string{"state", "baseRefName", "headRefName", "isCrossRepository", "headRepositoryOwner"})

	query := fmt.Sprintf(`
	query PullRequestForBranch($owner: String!, $repo: String!, $headRefName: String!, $states: [PullRequestState!]) {
		repository(owner: $owner, name: $repo) {
			pullRequests(headRefName: $headRefName, states: $states, first: 30, orderBy: { field: CREATED_AT, direction: DESC }) {
				nodes {%s}
			}
			defaultBranchRef { name }
		}
	}`, api.PullRequestGraphQL(fieldSet.ToSlice()))

	variables := map[string]any{
		"owner":       prRefs.BaseRepo().RepoOwner(),
		"repo":        prRefs.BaseRepo().RepoName(),
		"headRefName": prRefs.UnqualifiedHeadRef(),
		"states":      stateFilters,
	}

	var resp response
	client := api.NewClientFromHTTP(httpClient)
	err := client.GraphQL(prRefs.BaseRepo().RepoHost(), query, variables, &resp)
	if err != nil {
		return nil, err
	}

	prs := resp.Repository.PullRequests.Nodes
	sort.SliceStable(prs, func(a, b int) bool {
		return prs[a].State == "OPEN" && prs[b].State != "OPEN"
	})

	for _, pr := range prs {
		// 当 head 是默认分支时，显示已合并或已关闭的 PR 并不合理。
		// https://github.com/cli/cli/issues/4263
		isNotClosedOrMergedWhenHeadIsDefault := pr.State == "OPEN" || resp.Repository.DefaultBranchRef.Name != prRefs.QualifiedHeadRef()
		if prRefs.Matches(pr.BaseRefName, pr.HeadLabel()) && isNotClosedOrMergedWhenHeadIsDefault {
			return &pr, nil
		}
	}

	return nil, &NotFoundError{fmt.Errorf("未找到分支 %q 的任何拉取请求(PR)", prRefs.QualifiedHeadRef())}
}

func preloadPrReviews(httpClient *http.Client, repo ghrepo.Interface, pr *api.PullRequest) error {
	if !pr.Reviews.PageInfo.HasNextPage {
		return nil
	}

	type response struct {
		Node struct {
			PullRequest struct {
				Reviews api.PullRequestReviews `graphql:"reviews(first: 100, after: $endCursor)"`
			} `graphql:"...on PullRequest"`
		} `graphql:"node(id: $id)"`
	}

	variables := map[string]any{
		"id":        githubv4.ID(pr.ID),
		"endCursor": githubv4.String(pr.Reviews.PageInfo.EndCursor),
	}

	gql := api.NewClientFromHTTP(httpClient)

	for {
		var query response
		err := gql.Query(repo.RepoHost(), "ReviewsForPullRequest", &query, variables)
		if err != nil {
			return err
		}

		pr.Reviews.Nodes = append(pr.Reviews.Nodes, query.Node.PullRequest.Reviews.Nodes...)
		pr.Reviews.TotalCount = len(pr.Reviews.Nodes)

		if !query.Node.PullRequest.Reviews.PageInfo.HasNextPage {
			break
		}
		variables["endCursor"] = githubv4.String(query.Node.PullRequest.Reviews.PageInfo.EndCursor)
	}

	pr.Reviews.PageInfo.HasNextPage = false
	return nil
}

func preloadPrComments(client *http.Client, repo ghrepo.Interface, pr *api.PullRequest) error {
	if !pr.Comments.PageInfo.HasNextPage {
		return nil
	}

	type response struct {
		Node struct {
			PullRequest struct {
				Comments api.Comments `graphql:"comments(first: 100, after: $endCursor)"`
			} `graphql:"...on PullRequest"`
		} `graphql:"node(id: $id)"`
	}

	variables := map[string]any{
		"id":        githubv4.ID(pr.ID),
		"endCursor": githubv4.String(pr.Comments.PageInfo.EndCursor),
	}

	gql := api.NewClientFromHTTP(client)

	for {
		var query response
		err := gql.Query(repo.RepoHost(), "CommentsForPullRequest", &query, variables)
		if err != nil {
			return err
		}

		pr.Comments.Nodes = append(pr.Comments.Nodes, query.Node.PullRequest.Comments.Nodes...)
		pr.Comments.TotalCount = len(pr.Comments.Nodes)

		if !query.Node.PullRequest.Comments.PageInfo.HasNextPage {
			break
		}
		variables["endCursor"] = githubv4.String(query.Node.PullRequest.Comments.PageInfo.EndCursor)
	}

	pr.Comments.PageInfo.HasNextPage = false
	return nil
}

func preloadPrClosingIssuesReferences(client *http.Client, repo ghrepo.Interface, pr *api.PullRequest) error {
	if !pr.ClosingIssuesReferences.PageInfo.HasNextPage {
		return nil
	}

	type response struct {
		Node struct {
			PullRequest struct {
				ClosingIssuesReferences api.ClosingIssuesReferences `graphql:"closingIssuesReferences(first: 100, after: $endCursor)"`
			} `graphql:"...on PullRequest"`
		} `graphql:"node(id: $id)"`
	}

	variables := map[string]any{
		"id":        githubv4.ID(pr.ID),
		"endCursor": githubv4.String(pr.ClosingIssuesReferences.PageInfo.EndCursor),
	}

	gql := api.NewClientFromHTTP(client)

	for {
		var query response
		err := gql.Query(repo.RepoHost(), "closingIssuesReferences", &query, variables)
		if err != nil {
			return err
		}

		pr.ClosingIssuesReferences.Nodes = append(pr.ClosingIssuesReferences.Nodes, query.Node.PullRequest.ClosingIssuesReferences.Nodes...)

		if !query.Node.PullRequest.ClosingIssuesReferences.PageInfo.HasNextPage {
			break
		}
		variables["endCursor"] = githubv4.String(query.Node.PullRequest.ClosingIssuesReferences.PageInfo.EndCursor)
	}

	pr.ClosingIssuesReferences.PageInfo.HasNextPage = false
	return nil
}

func preloadPrChecks(client *http.Client, repo ghrepo.Interface, pr *api.PullRequest) error {
	if len(pr.StatusCheckRollup.Nodes) == 0 {
		return nil
	}
	statusCheckRollup := &pr.StatusCheckRollup.Nodes[0].Commit.StatusCheckRollup.Contexts
	if !statusCheckRollup.PageInfo.HasNextPage {
		return nil
	}

	endCursor := statusCheckRollup.PageInfo.EndCursor

	type response struct {
		Node *api.PullRequest
	}

	query := fmt.Sprintf(`
	query PullRequestStatusChecks($id: ID!, $endCursor: String!) {
		node(id: $id) {
			...on PullRequest {
				%s
			}
		}
	}`, api.StatusCheckRollupGraphQLWithoutCountByState("$endCursor"))

	variables := map[string]any{
		"id": pr.ID,
	}

	apiClient := api.NewClientFromHTTP(client)
	for {
		variables["endCursor"] = endCursor
		var resp response
		err := apiClient.GraphQL(repo.RepoHost(), query, variables, &resp)
		if err != nil {
			return err
		}

		result := resp.Node.StatusCheckRollup.Nodes[0].Commit.StatusCheckRollup.Contexts
		statusCheckRollup.Nodes = append(
			statusCheckRollup.Nodes,
			result.Nodes...,
		)

		if !result.PageInfo.HasNextPage {
			break
		}
		endCursor = result.PageInfo.EndCursor
	}

	statusCheckRollup.PageInfo.HasNextPage = false
	return nil
}

type NotFoundError struct {
	error
}

func (err *NotFoundError) Unwrap() error {
	return err.error
}

func NewMockFinder(selector string, pr *api.PullRequest, repo ghrepo.Interface) *mockFinder {
	var err error
	if pr == nil {
		err = &NotFoundError{errors.New("未找到任何拉取请求(PR)")}
	}
	return &mockFinder{
		expectSelector: selector,
		pr:             pr,
		repo:           repo,
		err:            err,
	}
}

type mockFinder struct {
	called         bool
	expectSelector string
	expectFields   []string
	pr             *api.PullRequest
	repo           ghrepo.Interface
	err            error
}

func (m *mockFinder) Find(opts FindOptions) (*api.PullRequest, ghrepo.Interface, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	if m.expectSelector != opts.Selector {
		return nil, nil, fmt.Errorf("mockFinder: 期望的 selector 为 %q，实际为 %q", m.expectSelector, opts.Selector)
	}
	if len(m.expectFields) > 0 && !isEqualSet(m.expectFields, opts.Fields) {
		return nil, nil, fmt.Errorf("mockFinder: 期望的字段为 %v，实际为 %v", m.expectFields, opts.Fields)
	}
	if m.called {
		return nil, nil, errors.New("mockFinder 被使用了不止一次")
	}
	m.called = true

	if m.pr.HeadRepositoryOwner.Login == "" {
		// 默认伪装成同仓库 PR
		m.pr.HeadRepositoryOwner.Login = m.repo.RepoOwner()
	}

	return m.pr, m.repo, nil
}

func (m *mockFinder) ExpectFields(fields []string) {
	m.expectFields = fields
}

func isEqualSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	aCopy := make([]string, len(a))
	copy(aCopy, a)
	bCopy := make([]string, len(b))
	copy(bCopy, b)
	sort.Strings(aCopy)
	sort.Strings(bCopy)

	for i := range aCopy {
		if aCopy[i] != bCopy[i] {
			return false
		}
	}
	return true
}
