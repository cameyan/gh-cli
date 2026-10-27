package capi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/shurcooL/githubv4"
	"github.com/vmihailenco/msgpack/v5"
)

const AgentsHomeURL = "https://github.com/copilot/agents"

var defaultSessionsPerPage = 50

var ErrSessionNotFound = errors.New("未找到")

// session 是一个进行中的 agent 任务
type session struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	UserID           int64     `json:"user_id"`
	AgentID          int64     `json:"agent_id"`
	Logs             string    `json:"logs"`
	State            string    `json:"state"`
	OwnerID          uint64    `json:"owner_id"`
	RepoID           uint64    `json:"repo_id"`
	ResourceType     string    `json:"resource_type"`
	ResourceID       int64     `json:"resource_id"`
	ResourceGlobalID string    `json:"resource_global_id"`
	LastUpdatedAt    time.Time `json:"last_updated_at"`
	CreatedAt        time.Time `json:"created_at"`
	CompletedAt      time.Time `json:"completed_at"`
	EventURL         string    `json:"event_url"`
	EventType        string    `json:"event_type"`
	PremiumRequests  float64   `json:"premium_requests"`
	WorkflowRunID    uint64    `json:"workflow_run_id,omitempty"`
	Error            *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// 完整 pull request 的精简替身，因为使用完整的 api.PullRequest 类型
// 按 node ID 查询时在 union (actors) 上会失败
type sessionPullRequest struct {
	ID             string
	FullDatabaseID string
	Number         int
	Title          string
	State          string
	URL            string
	Body           string
	IsDraft        bool

	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	MergedAt  *time.Time

	Repository *api.PRRepository
}

// Session 是一个已填充完整信息的进行中 agent 任务
type Session struct {
	ID              string
	Name            string
	UserID          int64
	AgentID         int64
	Logs            string
	State           string
	OwnerID         uint64
	RepoID          uint64
	ResourceType    string
	ResourceID      int64
	LastUpdatedAt   time.Time
	CreatedAt       time.Time
	CompletedAt     time.Time
	EventURL        string
	EventType       string
	PremiumRequests float64
	WorkflowRunID   uint64
	Error           *SessionError

	PullRequest *api.PullRequest
	User        *api.GitHubUser
}

type SessionError struct {
	Code    string
	Message string
}

// SessionFields 定义了 Session 的 JSON 导出可用字段。
var SessionFields = []string{
	"id",
	"name",
	"state",
	"repository",
	"user",
	"createdAt",
	"updatedAt",
	"completedAt",
	"pullRequestNumber",
	"pullRequestUrl",
	"pullRequestTitle",
	"pullRequestState",
}

// ExportData 实现了用于 JSON 输出的 exportable 接口。
func (s *Session) ExportData(fields []string) map[string]any {
	data := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "id":
			data[f] = s.ID
		case "name":
			data[f] = s.Name
		case "state":
			data[f] = s.State
		case "repository":
			if s.PullRequest != nil && s.PullRequest.Repository != nil {
				data[f] = s.PullRequest.Repository.NameWithOwner
			} else {
				data[f] = nil
			}
		case "user":
			if s.User != nil {
				data[f] = s.User.Login
			} else {
				data[f] = nil
			}
		case "createdAt":
			if s.CreatedAt.IsZero() {
				data[f] = nil
			} else {
				data[f] = s.CreatedAt
			}
		case "updatedAt":
			if s.LastUpdatedAt.IsZero() {
				data[f] = nil
			} else {
				data[f] = s.LastUpdatedAt
			}
		case "completedAt":
			if s.CompletedAt.IsZero() {
				data[f] = nil
			} else {
				data[f] = s.CompletedAt
			}
		case "pullRequestNumber":
			if s.PullRequest != nil {
				data[f] = s.PullRequest.Number
			} else {
				data[f] = nil
			}
		case "pullRequestUrl":
			if s.PullRequest != nil {
				data[f] = s.PullRequest.URL
			} else {
				data[f] = nil
			}
		case "pullRequestTitle":
			if s.PullRequest != nil {
				data[f] = s.PullRequest.Title
			} else {
				data[f] = nil
			}
		case "pullRequestState":
			if s.PullRequest != nil {
				data[f] = s.PullRequest.State
			} else {
				data[f] = nil
			}
		default:
			data[f] = nil
		}
	}
	return data
}

type resource struct {
	ID                   string            `json:"id"`
	UserID               uint64            `json:"user_id"`
	ResourceType         string            `json:"resource_type"`
	ResourceID           int64             `json:"resource_id"`
	ResourceGlobalID     string            `json:"resource_global_id"`
	SessionCount         int               `json:"session_count"`
	SessionLastUpdatedAt int64             `json:"last_updated_at"`
	SessionState         string            `json:"state,omitempty"`
	ResourceState        string            `json:"resource_state"`
	Sessions             []resourceSession `json:"sessions"`
}

type resourceSession struct {
	SessionID            string `json:"id"`
	Name                 string `json:"name"`
	SessionState         string `json:"state,omitempty"`
	SessionLastUpdatedAt int64  `json:"last_updated_at"`
}

// ListLatestSessionsForViewer 列出已认证用户的所有 agent session，
// 数量不超过 limit。
func (c *CAPIClient) ListLatestSessionsForViewer(ctx context.Context, limit int) ([]*Session, error) {
	if limit == 0 {
		return nil, nil
	}

	sessionsURL, err := safeurl.JoinPathWithHostPrefix(c.capiBaseURL, "agents", "sessions")
	if err != nil {
		return nil, err
	}
	pageSize := defaultSessionsPerPage

	seenResources := make(map[int64]struct{})
	latestSessions := make([]session, 0, limit)
	for page := 1; ; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, sessionsURL.String(), http.NoBody)
		if err != nil {
			return nil, err
		}

		q := req.URL.Query()
		q.Set("page_size", strconv.Itoa(pageSize))
		q.Set("page_number", strconv.Itoa(page))
		q.Set("sort", "last_updated_at,desc")
		req.URL.RawQuery = q.Encode()

		res, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("列出 session 失败: %s", res.Status)
		}
		var response struct {
			Sessions []session `json:"sessions"`
		}
		if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
			return nil, fmt.Errorf("解码 session 响应失败: %w", err)
		}

		// 只处理新获取的这一页 session。
		pageSessions := response.Sessions

		// 按 resource ID 对 session 去重。
		// 由于 API 按最新优先返回，一旦见过某个 resource ID，
		// 就可以忽略它更旧的 session。
		for _, s := range pageSessions {
			if _, exists := seenResources[s.ResourceID]; exists {
				continue
			}

			// resource ID 为零是在 PR/resource 与 session 关联之前的临时情况。
			// 我们不应将这种情况标记为已见。
			if s.ResourceID != 0 {
				seenResources[s.ResourceID] = struct{}{}
			}

			latestSessions = append(latestSessions, s)
			if len(latestSessions) >= limit {
				break
			}
		}

		if len(response.Sessions) < pageSize || len(latestSessions) >= limit {
			break
		}
	}

	// 丢弃超出 limit 的部分
	if len(latestSessions) > limit {
		latestSessions = latestSessions[:limit]
	}

	result, err := c.hydrateSessionPullRequestsAndUsers(latestSessions)
	if err != nil {
		return nil, fmt.Errorf("获取 session 资源失败: %w", err)
	}

	return result, nil
}

// GetSession 按 ID 获取指定的 agent session。
func (c *CAPIClient) GetSession(ctx context.Context, id string) (*Session, error) {
	if id == "" {
		return nil, fmt.Errorf("缺少 session ID")
	}

	u, err := safeurl.JoinPathWithHostPrefix(c.capiBaseURL, "agents", "sessions", id)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, err
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusNotFound {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("获取 session 失败: %s", res.Status)
	}

	var rawSession session
	if err := json.NewDecoder(res.Body).Decode(&rawSession); err != nil {
		return nil, fmt.Errorf("解码 session 响应失败: %w", err)
	}

	sessions, err := c.hydrateSessionPullRequestsAndUsers([]session{rawSession})
	if err != nil {
		return nil, fmt.Errorf("获取 session 资源失败: %w", err)
	}

	return sessions[0], nil
}

// GetSessionLogs 按 ID 获取一个 agent session 的日志。
func (c *CAPIClient) GetSessionLogs(ctx context.Context, id string) ([]byte, error) {
	if id == "" {
		return nil, fmt.Errorf("缺少 session ID")
	}

	u, err := safeurl.JoinPathWithHostPrefix(c.capiBaseURL, "agents", "sessions", id, "logs")
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, err
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusNotFound {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("获取 session 失败: %s", res.Status)
	}

	return io.ReadAll(res.Body)
}

// ListSessionsByResourceID 获取与给定 resource 类型和 ID 关联的 session。
func (c *CAPIClient) ListSessionsByResourceID(ctx context.Context, resourceType string, resourceID int64, limit int) ([]*Session, error) {
	if resourceType == "" || resourceID == 0 {
		return nil, fmt.Errorf("缺少 resource 类型/ID")
	}

	if limit == 0 {
		return nil, nil
	}

	u, err := safeurl.JoinPathWithHostPrefix(c.capiBaseURL, "agents", "resource", resourceType, strconv.FormatInt(resourceID, 10))
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, err
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("列出 session 失败: %s", res.Status)
	}

	var response resource
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("解码 session 响应失败: %w", err)
	}

	sessions := make([]session, 0, len(response.Sessions))
	for _, s := range response.Sessions {
		session := session{
			ID:               s.SessionID,
			Name:             s.Name,
			UserID:           int64(response.UserID),
			ResourceType:     response.ResourceType,
			ResourceID:       response.ResourceID,
			ResourceGlobalID: response.ResourceGlobalID,
			State:            s.SessionState,
		}
		if s.SessionLastUpdatedAt != 0 {
			session.LastUpdatedAt = time.Unix(s.SessionLastUpdatedAt, 0).UTC()
		}
		sessions = append(sessions, session)
	}

	result, err := c.hydrateSessionPullRequestsAndUsers(sessions)
	if err != nil {
		return nil, fmt.Errorf("获取 session 资源失败: %w", err)
	}
	return result, nil
}

// hydrateSessionPullRequestsAndUsers 在 session 中填充 pull request 和用户信息
func (c *CAPIClient) hydrateSessionPullRequestsAndUsers(sessions []session) ([]*Session, error) {
	if len(sessions) == 0 {
		return nil, nil
	}

	prNodeIds := make([]string, 0, len(sessions))
	userNodeIds := make([]string, 0, len(sessions))
	for _, session := range sessions {
		if session.ResourceType == "pull" {
			prNodeID := session.ResourceGlobalID
			// TODO: 由于 API 应该始终会返回 resource global ID，
			// 这段代码或许可以移除。
			if session.ResourceGlobalID == "" {
				prNodeID = generatePullRequestNodeID(int64(session.RepoID), session.ResourceID)
			}
			if !slices.Contains(prNodeIds, prNodeID) {
				prNodeIds = append(prNodeIds, prNodeID)
			}
		}

		userNodeId := generateUserNodeID(session.UserID)
		if !slices.Contains(userNodeIds, userNodeId) {
			userNodeIds = append(userNodeIds, userNodeId)
		}
	}
	apiClient := api.NewClientFromHTTP(c.httpClient)

	var resp struct {
		Nodes []struct {
			TypeName    string             `graphql:"__typename"`
			PullRequest sessionPullRequest `graphql:"... on PullRequest"`
			User        api.GitHubUser     `graphql:"... on User"`
		} `graphql:"nodes(ids: $ids)"`
	}

	ids := make([]string, 0, len(prNodeIds)+len(userNodeIds))
	ids = append(ids, prNodeIds...)
	ids = append(ids, userNodeIds...)

	// TODO 处理分页
	err := apiClient.Query(c.host, "FetchPRsAndUsersForAgentTaskSessions", &resp, map[string]any{
		"ids": ids,
	})

	if err != nil {
		return nil, err
	}

	prMap := make(map[string]*api.PullRequest, len(prNodeIds))
	userMap := make(map[int64]*api.GitHubUser, len(userNodeIds))
	for _, node := range resp.Nodes {
		switch node.TypeName {
		case "User":
			userMap[node.User.DatabaseID] = &node.User
		case "PullRequest":
			prMap[node.PullRequest.FullDatabaseID] = &api.PullRequest{
				ID:             node.PullRequest.ID,
				FullDatabaseID: node.PullRequest.FullDatabaseID,
				Number:         node.PullRequest.Number,
				Title:          node.PullRequest.Title,
				State:          node.PullRequest.State,
				IsDraft:        node.PullRequest.IsDraft,
				URL:            node.PullRequest.URL,
				Body:           node.PullRequest.Body,
				CreatedAt:      node.PullRequest.CreatedAt,
				UpdatedAt:      node.PullRequest.UpdatedAt,
				ClosedAt:       node.PullRequest.ClosedAt,
				MergedAt:       node.PullRequest.MergedAt,
				Repository:     node.PullRequest.Repository,
			}
		}
	}

	newSessions := make([]*Session, 0, len(sessions))
	for _, s := range sessions {
		newSession := fromAPISession(s)
		newSession.PullRequest = prMap[strconv.FormatInt(s.ResourceID, 10)]
		newSession.User = userMap[s.UserID]
		newSessions = append(newSessions, newSession)
	}

	return newSessions, nil
}

// GetPullRequestDatabaseID 根据 pull request 在仓库中的编号获取其数据库 ID 和 URL。
func (c *CAPIClient) GetPullRequestDatabaseID(ctx context.Context, hostname string, owner string, repo string, number int) (int64, string, error) {
	// TODO: 更好地处理 int，这样我们就无需做边界检查，
	// 既能确保不可能发生 panic，也不会触发 CodeQL 告警。
	if number <= 0 || number > math.MaxInt32 {
		return 0, "", fmt.Errorf("PR 编号 %d 超出范围", number)
	}

	var resp struct {
		Repository struct {
			PullRequest struct {
				FullDatabaseID string `graphql:"fullDatabaseId"`
				URL            string `graphql:"url"`
			} `graphql:"pullRequest(number: $number)"`
		} `graphql:"repository(owner: $owner, name: $repo)"`
	}

	variables := map[string]any{
		"owner":  githubv4.String(owner),
		"repo":   githubv4.String(repo),
		"number": githubv4.Int(number),
	}

	apiClient := api.NewClientFromHTTP(c.httpClient)
	if err := apiClient.Query(hostname, "GetPullRequestFullDatabaseID", &resp, variables); err != nil {
		return 0, "", err
	}

	databaseID, err := strconv.ParseInt(resp.Repository.PullRequest.FullDatabaseID, 10, 64)
	if err != nil {
		return 0, "", err
	}
	return databaseID, resp.Repository.PullRequest.URL, nil
}

// generatePullRequestNodeID 将 int64 类型的 databaseID 和 repoID 转换为 GraphQL Node ID 格式，
// pull request 使用 "PR_" 前缀
func generatePullRequestNodeID(repoID, pullRequestID int64) string {
	buf := bytes.Buffer{}
	parts := []int64{0, repoID, pullRequestID}

	encoder := msgpack.NewEncoder(&buf)
	encoder.UseCompactInts(true)

	if err := encoder.Encode(parts); err != nil {
		panic(err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(buf.Bytes())

	return "PR_" + encoded
}

func generateUserNodeID(userID int64) string {
	buf := bytes.Buffer{}
	parts := []int64{0, userID}

	encoder := msgpack.NewEncoder(&buf)
	encoder.UseCompactInts(true)

	if err := encoder.Encode(parts); err != nil {
		panic(err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(buf.Bytes())

	return "U_" + encoded
}

func fromAPISession(s session) *Session {
	result := Session{
		ID:              s.ID,
		Name:            s.Name,
		UserID:          s.UserID,
		AgentID:         s.AgentID,
		Logs:            s.Logs,
		State:           s.State,
		OwnerID:         s.OwnerID,
		RepoID:          s.RepoID,
		ResourceType:    s.ResourceType,
		ResourceID:      s.ResourceID,
		LastUpdatedAt:   s.LastUpdatedAt,
		CreatedAt:       s.CreatedAt,
		CompletedAt:     s.CompletedAt,
		EventURL:        s.EventURL,
		EventType:       s.EventType,
		PremiumRequests: s.PremiumRequests,
		WorkflowRunID:   s.WorkflowRunID,
	}
	if s.Error != nil {
		result.Error = &SessionError{
			Code:    s.Error.Code,
			Message: s.Error.Message,
		}
	}
	return &result
}
