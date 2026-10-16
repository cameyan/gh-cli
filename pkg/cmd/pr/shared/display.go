package shared

import (
	"fmt"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
)

func StateTitleWithColor(cs *iostreams.ColorScheme, pr api.PullRequest) string {
	prStateColorFunc := cs.ColorFromString(ColorForPRState(pr))
	if pr.State == "OPEN" && pr.IsDraft {
		return prStateColorFunc("草稿")
	}
	return prStateColorFunc(text.Title(pr.State))
}

func PrStateWithDraft(pr *api.PullRequest) string {
	if pr.IsDraft && pr.State == "OPEN" {
		return "DRAFT"
	}

	return pr.State
}

func ColorForPRState(pr api.PullRequest) string {
	switch pr.State {
	case "OPEN":
		if pr.IsDraft {
			return "gray"
		}
		return "green"
	case "CLOSED":
		return "red"
	case "MERGED":
		return "magenta"
	default:
		return ""
	}
}

func ColorForIssueState(issue api.Issue) string {
	switch issue.State {
	case "OPEN":
		return "green"
	case "CLOSED":
		if issue.StateReason == "NOT_PLANNED" {
			return "gray"
		}
		return "magenta"
	default:
		return ""
	}
}

func PrintHeader(io *iostreams.IOStreams, s string) {
	fmt.Fprintln(io.Out, io.ColorScheme().Bold(s))
}

func PrintMessage(io *iostreams.IOStreams, s string) {
	fmt.Fprintln(io.Out, io.ColorScheme().Muted(s))
}

func ListNoResults(repoName string, itemName string, hasFilters bool) error {
	if hasFilters {
		return cmdutil.NewNoResultsError(fmt.Sprintf("%s 中没有匹配您搜索的%s", repoName, itemName))
	}
	return cmdutil.NewNoResultsError(fmt.Sprintf("%s 中没有 open 状态的%s", repoName, itemName))
}

func ListHeader(repoName string, itemName string, matchCount int, totalMatchCount int, hasFilters bool) string {
	if hasFilters {
		return fmt.Sprintf("显示 %s 中匹配您搜索的 %d 条，共 %d 条%s", repoName, matchCount, totalMatchCount, text.Pluralize(totalMatchCount, itemName))
	}

	return fmt.Sprintf("显示 %s 中的 %d 条，共 %d 条%s", repoName, matchCount, totalMatchCount, text.Pluralize(totalMatchCount, fmt.Sprintf("open %s", itemName)))
}

func PrCheckStatusSummaryWithColor(cs *iostreams.ColorScheme, checks api.PullRequestChecksStatus) string {
	var summary = cs.Muted("没有检查")
	if checks.Total > 0 {
		if checks.Failing > 0 {
			if checks.Failing == checks.Total {
				summary = cs.Red("× 所有检查均失败")
			} else {
				summary = cs.Redf("× %d/%d 个检查失败", checks.Failing, checks.Total)
			}
		} else if checks.Pending > 0 {
			summary = cs.Yellow("- 检查等待中")
		} else if checks.Passing == checks.Total {
			summary = cs.Green("✓ 检查通过")
		}
	}
	return summary
}
