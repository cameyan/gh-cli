package capi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cli/cli/v2/internal/safeurl"
)

const defaultEventType = "gh_cli"

// Job 表示一个编码 agent 的任务。用于请求新的 session。
type Job struct {
	ID                string          `json:"job_id,omitempty"`
	SessionID         string          `json:"session_id,omitempty"`
	ProblemStatement  string          `json:"problem_statement,omitempty"`
	CustomAgent       string          `json:"custom_agent,omitempty"`
	EventType         string          `json:"event_type,omitempty"`
	ContentFilterMode string          `json:"content_filter_mode,omitempty"`
	Status            string          `json:"status,omitempty"`
	Result            string          `json:"result,omitempty"`
	Actor             *JobActor       `json:"actor,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	PullRequest       *JobPullRequest `json:"pull_request,omitempty"`
	WorkflowRun       *struct {
		ID string `json:"id"`
	} `json:"workflow_run,omitempty"`
	ErrorInfo *JobError `json:"error,omitempty"`
}

type JobActor struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type JobPullRequest struct {
	ID      int64  `json:"id"`
	Number  int    `json:"number"`
	BaseRef string `json:"base_ref,omitempty"`
}

type JobError struct {
	Message            string `json:"message"`
	ResponseStatusCode int    `json:"response_status_code,string"`
	Service            string `json:"service"`
}

func (c *CAPIClient) jobsBasePathV1() string {
	return c.capiBaseURL + "/agents/swe/v1/jobs"
}

// CreateJob 使用 v1 Jobs API 排队一个新 job。它可能返回也可能不返回 Pull Request 信息。
// 如果需要 Pull Request 信息，则必须随后使用 job ID 轮询 GetJob。
func (c *CAPIClient) CreateJob(ctx context.Context, owner, repo, problemStatement, baseBranch, customAgent string) (*Job, error) {
	if owner == "" || repo == "" {
		return nil, errors.New("必须提供 owner 和 repo")
	}
	if problemStatement == "" {
		return nil, errors.New("必须提供 problem statement")
	}

	u, err := safeurl.JoinPathWithHostPrefix(c.jobsBasePathV1(), owner, repo)
	if err != nil {
		return nil, err
	}

	prOpts := JobPullRequest{}
	if baseBranch != "" {
		prOpts.BaseRef = "refs/heads/" + baseBranch
	}

	payload := &Job{
		ProblemStatement: problemStatement,
		CustomAgent:      customAgent,
		EventType:        defaultEventType,
		PullRequest:      &prOpts,
	}

	b, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)

	var j Job
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&j); err != nil {
		if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK { // 接受 201 或 200
			// 当出现类似未授权 (401) 的错误时会发生这种情况。
			statusText := fmt.Sprintf("%d %s", res.StatusCode, http.StatusText(res.StatusCode))
			return nil, fmt.Errorf("创建 job 失败: %s", statusText)
		}
		return nil, fmt.Errorf("解码创建 job 响应失败: %w", err)
	}

	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK { // 接受 201 或 200
		statusText := fmt.Sprintf("%d %s", res.StatusCode, http.StatusText(res.StatusCode))

		// 如果响应中内嵌了错误信息，我们可以直接使用它。
		// TODO: 这种情况真的会发生吗？
		if j.ErrorInfo != nil {
			return nil, fmt.Errorf("创建 job 失败: %s: %s", statusText, j.ErrorInfo.Message)
		}

		// 如果响应中没有内嵌错误信息，
		// 则尝试将响应本身解码为 jobError。
		var errInfo JobError
		if err := json.NewDecoder(bytes.NewReader(body)).Decode(&errInfo); err != nil {
			return nil, fmt.Errorf("创建 job 失败: %s", statusText)
		}

		return nil, fmt.Errorf("创建 job 失败: %s: %s", statusText, errInfo.Message)
	}

	return &j, nil
}

// GetJob 获取一个 agent job
func (c *CAPIClient) GetJob(ctx context.Context, owner, repo, jobID string) (*Job, error) {
	if owner == "" || repo == "" || jobID == "" {
		return nil, errors.New("必须提供 owner、repo 和 jobID")
	}
	u, err := safeurl.JoinPathWithHostPrefix(c.jobsBasePathV1(), owner, repo, jobID)
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
		// 规范化为 "<code> <text>" 形式
		statusText := fmt.Sprintf("%d %s", res.StatusCode, http.StatusText(res.StatusCode))
		return nil, fmt.Errorf("获取 job 失败: %s", statusText)
	}
	var j Job
	if err := json.NewDecoder(res.Body).Decode(&j); err != nil {
		return nil, fmt.Errorf("解码获取 job 响应失败: %w", err)
	}
	return &j, nil
}
