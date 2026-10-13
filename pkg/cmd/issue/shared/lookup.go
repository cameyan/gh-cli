package shared

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	o "github.com/cli/cli/v2/pkg/option"
	"github.com/cli/cli/v2/pkg/set"
	"golang.org/x/sync/errgroup"
)

var issueURLRE = regexp.MustCompile(`^/([^/]+)/([^/]+)/(?:issues|pull)/(\d+)`)

func ParseIssuesFromArgs(args []string) ([]int, o.Option[ghrepo.Interface], error) {
	var repo o.Option[ghrepo.Interface]
	issueNumbers := make([]int, len(args))

	for i, arg := range args {
		// 对每个参数, 解析议题编号和可选的仓库
		issueNumber, issueRepo, err := ParseIssueFromArg(arg)
		if err != nil {
			return nil, o.None[ghrepo.Interface](), err
		}

		// 如果这是找到的第一个议题仓库, 则需要设置它
		if repo.IsNone() {
			repo = issueRepo
		}

		// 如果返回了议题仓库, 则需要检查它是否与之前的是同一个
		if issueRepo.IsSome() && repo.IsSome() {
			// 由于上面已检查过存在性, 因此解包是安全的
			if !ghrepo.IsSame(repo.Unwrap(), issueRepo.Unwrap()) {
				return nil, o.None[ghrepo.Interface](), fmt.Errorf(
					"多个议题必须位于同一仓库中: 找到 %q, 期望 %q",
					ghrepo.FullName(issueRepo.Unwrap()),
					ghrepo.FullName(repo.Unwrap()),
				)
			}
		}

		// 将议题编号添加到列表中
		issueNumbers[i] = issueNumber
	}

	return issueNumbers, repo, nil
}

func ParseIssueFromArg(arg string) (int, o.Option[ghrepo.Interface], error) {
	issueLocator := tryParseIssueFromURL(arg)
	if issueLocator, present := issueLocator.Value(); present {
		return issueLocator.issueNumber, o.Some(issueLocator.repo), nil
	}

	issueNumber, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
	if err != nil {
		return 0, o.None[ghrepo.Interface](), fmt.Errorf("无效的议题格式: %q", arg)
	}

	return issueNumber, o.None[ghrepo.Interface](), nil
}

type issueLocator struct {
	issueNumber int
	repo        ghrepo.Interface
}

// tryParseIssueFromURL 尝试从 URL 解析出议题编号和仓库。
func tryParseIssueFromURL(maybeURL string) o.Option[issueLocator] {
	u, err := url.Parse(maybeURL)
	if err != nil {
		return o.None[issueLocator]()
	}

	if u.Scheme != "https" && u.Scheme != "http" {
		return o.None[issueLocator]()
	}

	m := issueURLRE.FindStringSubmatch(u.Path)
	if m == nil {
		return o.None[issueLocator]()
	}

	repo := ghrepo.NewWithHost(m[1], m[2], u.Hostname())
	issueNumber, _ := strconv.Atoi(m[3])
	return o.Some(issueLocator{
		issueNumber: issueNumber,
		repo:        repo,
	})
}

type PartialLoadError struct {
	error
}

// FindIssuesOrPRs 使用指定的字段加载 1 个或多个议题或拉取请求。如果某些字段
// 无法通过 GraphQL 获取, 则会返回非 nil 的 issues 以及一个 *PartialLoadError。
func FindIssuesOrPRs(httpClient *http.Client, repo ghrepo.Interface, issueNumbers []int, fields []string) ([]*api.Issue, error) {
	issuesChan := make(chan *api.Issue, len(issueNumbers))
	g := errgroup.Group{}
	for _, num := range issueNumbers {
		issueNumber := num
		g.Go(func() error {
			issue, err := FindIssueOrPR(httpClient, repo, issueNumber, fields)
			if err != nil {
				return err
			}

			issuesChan <- issue
			return nil
		})
	}

	err := g.Wait()
	close(issuesChan)

	if err != nil {
		return nil, err
	}

	issues := make([]*api.Issue, 0, len(issueNumbers))
	for issue := range issuesChan {
		issues = append(issues, issue)
	}

	return issues, nil
}

func FindIssueOrPR(httpClient *http.Client, repo ghrepo.Interface, number int, fields []string) (*api.Issue, error) {
	fieldSet := set.NewStringSet()
	fieldSet.AddValues(fields)

	var getProjectItems bool
	if fieldSet.Contains("projectItems") {
		getProjectItems = true
		fieldSet.Remove("projectItems")
		fieldSet.Add("number")
	}

	fields = fieldSet.ToSlice()

	type response struct {
		Repository struct {
			HasIssuesEnabled bool
			Issue            *api.Issue
		}
	}

	query := fmt.Sprintf(`
	query IssueByNumber($owner: String!, $repo: String!, $number: Int!) {
		repository(owner: $owner, name: $repo) {
			hasIssuesEnabled
			issue: issueOrPullRequest(number: $number) {
				__typename
				...on Issue{%[1]s}
				...on PullRequest{%[2]s}
			}
		}
	}`, api.IssueGraphQL(fields), api.PullRequestGraphQL(fields))

	variables := map[string]any{
		"owner":  repo.RepoOwner(),
		"repo":   repo.RepoName(),
		"number": number,
	}

	var resp response
	client := api.NewClientFromHTTP(httpClient)
	if err := client.GraphQL(repo.RepoHost(), query, variables, &resp); err != nil {
		if gerr, ok := errors.AsType[api.GraphQLError](err); ok {
			if gerr.Match("NOT_FOUND", "repository.issue") && !resp.Repository.HasIssuesEnabled {
				return nil, fmt.Errorf("'%s' 仓库已禁用议题", ghrepo.FullName(repo))
			} else if gerr.Match("FORBIDDEN", "repository.issue.projectCards.") {
				issue := resp.Repository.Issue
				// 由于权限问题, 移除项目卡片的 nil 条目
				projects := make([]*api.ProjectInfo, 0, len(issue.ProjectCards.Nodes))
				for _, p := range issue.ProjectCards.Nodes {
					if p != nil {
						projects = append(projects, p)
					}
				}
				issue.ProjectCards.Nodes = projects
				return issue, &PartialLoadError{err}
			}
		}
		return nil, err
	}

	if resp.Repository.Issue == nil {
		return nil, errors.New("未找到议题, 但 GraphQL 未报告错误")
	}

	if getProjectItems {
		apiClient := api.NewClientFromHTTP(httpClient)
		err := api.ProjectsV2ItemsForIssue(apiClient, repo, resp.Repository.Issue)
		if err != nil && !api.ProjectsV2IgnorableError(err) {
			return nil, err
		}
	}

	return resp.Repository.Issue, nil
}

// ResolveIssueRef 解析议题引用(编号或 URL)并返回其
// 节点 ID。指向与 baseRepo 不同主机的引用会被
// 拒绝, 因为关系变更需要来自基础主机的 ID。
func ResolveIssueRef(client *api.Client, baseRepo ghrepo.Interface, ref string) (string, error) {
	number, repo, err := ParseIssueFromArg(ref)
	if err != nil {
		return "", err
	}

	targetRepo := baseRepo
	if r, ok := repo.Value(); ok {
		if r.RepoHost() != baseRepo.RepoHost() {
			return "", fmt.Errorf("议题引用 %q 的主机 (%s) 与当前仓库的主机 (%s) 不同", ref, r.RepoHost(), baseRepo.RepoHost())
		}
		targetRepo = r
	}

	return api.IssueNodeID(client, targetRepo, number)
}

// ResolveIssueTypeName 通过获取仓库的可用类型,
// 将议题类型名称解析为其节点 ID。
func ResolveIssueTypeName(client *api.Client, repo ghrepo.Interface, typeName string) (string, error) {
	issueTypes, err := api.RepoIssueTypes(client, repo)
	if err != nil {
		return "", err
	}

	typeNames := make([]string, len(issueTypes))
	for i, t := range issueTypes {
		typeNames[i] = t.Name
		if strings.EqualFold(t.Name, typeName) {
			return t.ID, nil
		}
	}

	return "", fmt.Errorf("未找到类型 %q; 可用类型: %s", typeName, strings.Join(typeNames, ", "))
}
