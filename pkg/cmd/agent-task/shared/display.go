package shared

import (
	"github.com/cli/cli/v2/pkg/cmd/agent-task/capi"
	"github.com/cli/cli/v2/pkg/iostreams"
)

// ColorFuncForSessionState 返回一个为 session 状态着色的函数
func ColorFuncForSessionState(s capi.Session, cs *iostreams.ColorScheme) func(string) string {
	var stateColor func(string) string
	switch s.State {
	case "completed":
		stateColor = cs.Green
	case "cancelled":
		stateColor = cs.Muted
	case "in_progress", "queued":
		stateColor = cs.Yellow
	case "failed":
		stateColor = cs.Red
	default:
		stateColor = cs.Muted
	}

	return stateColor
}

// SessionStateString 返回给定 session 状态的可读/首字母大写形式。
func SessionStateString(state string) string {
	switch state {
	case "queued":
		return "排队中"
	case "in_progress":
		return "进行中"
	case "completed":
		return "可评审"
	case "failed":
		return "失败"
	case "idle":
		return "空闲"
	case "waiting_for_user":
		return "等待用户"
	case "timed_out":
		return "已超时"
	case "cancelled":
		return "已取消"
	default:
		return state
	}
}

type ColorFunc func(string) string

func SessionSymbol(cs *iostreams.ColorScheme, state string) string {
	noColor := func(s string) string { return s }
	switch state {
	case "completed":
		return cs.SuccessIconWithColor(noColor)
	case "failed", "timed_out", "cancelled":
		return cs.FailureIconWithColor(noColor)
	default:
		return "-"
	}
}
