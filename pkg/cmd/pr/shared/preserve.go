package shared

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
)

func PreserveInput(io *iostreams.IOStreams, state *IssueMetadataState, createErr *error) func() {
	return func() {
		if !state.IsDirty() {
			return
		}

		if *createErr == nil {
			return
		}

		if cmdutil.IsUserCancellation(*createErr) {
			// 这些错误是用户主动取消导致的
			return
		}

		out := io.ErrOut

		// 这个额外的换行用于避免追加到调查(survey)行的末尾
		fmt.Fprintln(out)

		data, err := json.Marshal(state)
		if err != nil {
			fmt.Fprintf(out, "保存输入到文件失败: %s\n", err)
			fmt.Fprintln(out, "本应保存的内容:")
			fmt.Fprintf(out, "%v\n", state)
			return
		}

		tmpfile, err := io.TempFile(os.TempDir(), "gh*.json")
		if err != nil {
			fmt.Fprintf(out, "保存输入到文件失败: %s\n", err)
			fmt.Fprintln(out, "本应保存的内容:")
			fmt.Fprintf(out, "%v\n", state)
			return
		}

		_, err = tmpfile.Write(data)
		if err != nil {
			fmt.Fprintf(out, "保存输入到文件失败: %s\n", err)
			fmt.Fprintln(out, "本应保存的内容:")
			fmt.Fprintln(out, string(data))
			return
		}

		cs := io.ColorScheme()

		issueType := "pr"
		if state.Type == IssueMetadata {
			issueType = "issue"
		}

		fmt.Fprintf(out, "%s 操作失败。恢复方法: gh %s create --recover %s\n", cs.FailureIcon(), issueType, tmpfile.Name())

		// 在实际错误之前留出一些空白
		fmt.Fprintln(out)
	}
}
