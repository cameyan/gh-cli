package codespace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/spf13/cobra"
)

type CodespaceSelector struct {
	api apiClient

	repoName      string
	codespaceName string
	repoOwner     string
}

var errNoFilteredCodespaces = errors.New("没有符合筛选条件的 codespace")

// AddCodespaceSelector 为给定命令添加选择 codespace 的持久标志，并返回应用这些标志的 CodespaceSelector
func AddCodespaceSelector(cmd *cobra.Command, api apiClient) *CodespaceSelector {
	cs := &CodespaceSelector{api: api}

	cmd.PersistentFlags().StringVarP(&cs.codespaceName, "codespace", "c", "", "codespace 的名称")
	cmd.PersistentFlags().StringVarP(&cs.repoName, "repo", "R", "", "按仓库名称 (user/repo) 筛选 codespace 选择")
	cmd.PersistentFlags().StringVar(&cs.repoOwner, "repo-owner", "", "按仓库所有者 (用户名或组织) 筛选 codespace 选择")

	cmd.MarkFlagsMutuallyExclusive("codespace", "repo")
	cmd.MarkFlagsMutuallyExclusive("codespace", "repo-owner")

	return cs
}

func (cs *CodespaceSelector) Select(ctx context.Context) (codespace *api.Codespace, err error) {
	if cs.codespaceName != "" {
		codespace, err = cs.api.GetCodespace(ctx, cs.codespaceName, true)
		if err != nil {
			return nil, fmt.Errorf("获取 codespace 完整详情失败: %w", err)
		}
	} else {
		codespaces, err := cs.fetchCodespaces(ctx)
		if err != nil {
			return nil, err
		}

		codespace, err = cs.chooseCodespace(ctx, codespaces)
		if err != nil {
			return nil, err
		}
	}

	if codespace.PendingOperation {
		return nil, fmt.Errorf(
			"codespace 存在待处理操作，已被禁用: %s",
			codespace.PendingOperationDisabledReason,
		)
	}

	return codespace, nil
}

func (cs *CodespaceSelector) SelectName(ctx context.Context) (string, error) {
	if cs.codespaceName != "" {
		return cs.codespaceName, nil
	}

	codespaces, err := cs.fetchCodespaces(ctx)
	if err != nil {
		return "", err
	}

	codespace, err := cs.chooseCodespace(ctx, codespaces)
	if err != nil {
		return "", err
	}

	return codespace.Name, nil
}

func (cs *CodespaceSelector) fetchCodespaces(ctx context.Context) (codespaces []*api.Codespace, err error) {
	codespaces, err = cs.api.ListCodespaces(ctx, api.ListCodespacesOptions{})
	if err != nil {
		return nil, fmt.Errorf("获取 codespace 时出错: %w", err)
	}

	if len(codespaces) == 0 {
		return nil, errNoCodespaces
	}

	// Note that repo filtering done here can also be done in api.ListCodespaces.
	// We do it here instead so that we can differentiate no codespaces in general vs. none after filtering.
	// 注意: 这里的仓库过滤也可以在 api.ListCodespaces 中完成。
	// 放在这里是为了区分"总体上没有 codespace"与"过滤后没有 codespace"。
	if cs.repoName != "" {
		var filteredCodespaces []*api.Codespace
		for _, c := range codespaces {
			if !strings.EqualFold(c.Repository.FullName, cs.repoName) {
				continue
			}

			filteredCodespaces = append(filteredCodespaces, c)
		}

		codespaces = filteredCodespaces
	}

	if cs.repoOwner != "" {
		codespaces = filterCodespacesByRepoOwner(codespaces, cs.repoOwner)
	}

	if len(codespaces) == 0 {
		return nil, errNoFilteredCodespaces
	}

	return codespaces, err
}

func (cs *CodespaceSelector) chooseCodespace(ctx context.Context, codespaces []*api.Codespace) (codespace *api.Codespace, err error) {
	skipPromptForSingleOption := cs.repoName != ""
	codespace, err = chooseCodespaceFromList(ctx, codespaces, false, skipPromptForSingleOption)
	if err != nil {
		if err == errNoCodespaces {
			return nil, err
		}
		return nil, fmt.Errorf("选择 codespace 失败: %w", err)
	}

	return codespace, nil
}
