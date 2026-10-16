package shared

import (
	"encoding/json"
	"fmt"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/pkg/iostreams"
)

type metadataStateType int

const (
	IssueMetadata metadataStateType = iota
	PRMetadata
)

type IssueMetadataState struct {
	Type metadataStateType

	Draft bool

	// TODO ApiActorsSupported
	// ApiActorsSupported 表示该主机支持基于 actor 的 API (github.com, ghe.com)。
	// 为 true 时，mutation 直接使用登录名(login)而不是解析节点 ID。
	// 待 GHES 支持 replaceActorsForAssignable 与 requestReviewsByLogin mutation 后，
	// 可移除该 flag(并收敛为仅 actor 路径)。
	ApiActorsSupported bool

	Body  string
	Title string

	Template string

	Metadata      []string
	Reviewers     []string
	Assignees     []string
	Labels        []string
	ProjectTitles []string
	Milestones    []string

	MetadataResult *api.RepoMetadataResult

	dirty bool // 用户输入/输出是否修改过该状态
}

func (tb *IssueMetadataState) MarkDirty() {
	tb.dirty = true
}

func (tb *IssueMetadataState) IsDirty() bool {
	return tb.dirty || tb.HasMetadata()
}

func (tb *IssueMetadataState) HasMetadata() bool {
	return len(tb.Reviewers) > 0 ||
		len(tb.Assignees) > 0 ||
		len(tb.Labels) > 0 ||
		len(tb.ProjectTitles) > 0 ||
		len(tb.Milestones) > 0
}

func FillFromJSON(io *iostreams.IOStreams, recoverFile string, state *IssueMetadataState) error {
	var data []byte
	var err error
	data, err = io.ReadUserFile(recoverFile)
	if err != nil {
		return fmt.Errorf("读取文件 %s 失败: %w", recoverFile, err)
	}

	err = json.Unmarshal(data, state)
	if err != nil {
		return fmt.Errorf("JSON 解析失败: %w", err)
	}

	return nil
}
