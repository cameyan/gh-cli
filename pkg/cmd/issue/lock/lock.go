// Package lock 用于锁定和解除锁定 GitHub 议题与拉取请求上的对话。
//
// 每个拉取请求都是一个议题，但并非每个议题都是拉取请求。
// 因此，本包也用于 `cmd/pr`。
//
// 关于 "comments"(评论)、"conversations"(对话)与 "discussions"(讨论)命名的说明:
// GitHub 文档将议题或拉取请求上的一组评论称为对话。GitHub 讨论则指项目的
// "message board"(留言板)，可在其中发布公告、问题与回答。
package lock

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmd/issue/shared"
	issueShared "github.com/cli/cli/v2/pkg/cmd/issue/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/shurcooL/githubv4"
	"github.com/spf13/cobra"
)

type iprompter interface {
	Confirm(string, bool) (bool, error)
	Select(string, string, []string) (int, error)
}

// reasons 包含 GitHub 允许的所有锁定原因。
//
// 我们不直接构造 map，以便按字母顺序维护这些原因。
var reasons = []string{"off_topic", "resolved", "spam", "too_heated"}

var reasonsString = strings.Join(reasons, ", ")

var reasonsApi = []githubv4.LockReason{
	githubv4.LockReasonOffTopic,
	githubv4.LockReasonResolved,
	githubv4.LockReasonSpam,
	githubv4.LockReasonTooHeated,
}

// 如果未提供原因(空字符串)，reasonsMap 将返回 nil 值，因为它不包含在 map 中。
// 这样会将 GraphQL 中的 lock_reason 设置为 null。
var reasonsMap map[string]*githubv4.LockReason

func init() {
	reasonsMap = make(map[string]*githubv4.LockReason)
	for i, reason := range reasons {
		reasonsMap[reason] = &reasonsApi[i]
	}
}

type command struct {
	Name     string // 实际命令名称
	FullName string // 命令的完整名称
	Typename string // issue.Typename 的返回值
}

// `FullName` 应像句子开头一样首字母大写，因为它用于打印和错误信息中。
// 手动将其首字母大写，并在需要时调用 `ToLower`，比反过来更容易。
var aliasIssue = command{"issue", "议题", api.TypeIssue}
var aliasPr = command{"pr", "拉取请求", api.TypePullRequest}

var alias map[string]*command = map[string]*command{
	"issue":             &aliasIssue,
	"pr":                &aliasPr,
	api.TypeIssue:       &aliasIssue,
	api.TypePullRequest: &aliasPr,
}

// 对话可接受的锁定状态。这些用于打印语句中，因此使用字符串而不是布尔值。
const (
	Lock   = "Lock"
	Unlock = "Unlock"
)

func fields() []string {
	return []string{
		"activeLockReason", "id", "locked", "number", "title", "url",
	}
}

type LockOptions struct {
	HttpClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)
	Prompter   iprompter

	ParentCmd   string
	Reason      string
	IssueNumber int
	Interactive bool
}

func (opts *LockOptions) setCommonOptions(f *cmdutil.Factory, args []string) error {
	opts.IO = f.IOStreams
	opts.HttpClient = f.HttpClient
	opts.Config = f.Config

	issueNumber, baseRepo, err := shared.ParseIssueFromArg(args[0])
	if err != nil {
		return err
	}

	// 如果参数提供了基础仓库，则直接使用它。
	if baseRepo, present := baseRepo.Value(); present {
		opts.BaseRepo = func() (ghrepo.Interface, error) {
			return baseRepo, nil
		}
	} else {
		// 支持 `-R, --repo` 覆盖
		opts.BaseRepo = f.BaseRepo
	}

	opts.IssueNumber = issueNumber

	return nil
}

func NewCmdLock(f *cmdutil.Factory, parentName string, runF func(string, *LockOptions) error) *cobra.Command {
	opts := &LockOptions{
		ParentCmd: parentName,
		Prompter:  f.Prompter,
	}

	c := alias[opts.ParentCmd]
	short := fmt.Sprintf("锁定 %s 对话", strings.ToLower(c.FullName))

	cmd := &cobra.Command{
		Use:   "lock {<number> | <url>}",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.setCommonOptions(f, args); err != nil {
				return err
			}

			reasonProvided := cmd.Flags().Changed("reason")
			if reasonProvided {
				_, ok := reasonsMap[opts.Reason]
				if !ok {
					if opts.IO.IsStdoutTTY() {
						cs := opts.IO.ColorScheme()

						return cmdutil.FlagErrorf("%s 无效的原因: %v\n",
							cs.FailureIconWithColor(cs.Red), opts.Reason)
					} else {
						return fmt.Errorf("无效的原因 %s", opts.Reason)
					}
				}
			} else if opts.IO.CanPrompt() {
				opts.Interactive = true
			}

			if runF != nil {
				return runF(Lock, opts)
			}
			return lockRun(Lock, opts)
		},
	}

	msg := fmt.Sprintf("锁定对话的可选原因 (%v)。", reasonsString)

	cmd.Flags().StringVarP(&opts.Reason, "reason", "r", "", msg)
	return cmd
}

func NewCmdUnlock(f *cmdutil.Factory, parentName string, runF func(string, *LockOptions) error) *cobra.Command {
	opts := &LockOptions{ParentCmd: parentName}

	c := alias[opts.ParentCmd]
	short := fmt.Sprintf("解除锁定 %s 对话", strings.ToLower(c.FullName))

	cmd := &cobra.Command{
		Use:   "unlock {<number> | <url>}",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.setCommonOptions(f, args); err != nil {
				return err
			}

			if runF != nil {
				return runF(Unlock, opts)
			}
			return lockRun(Unlock, opts)
		},
	}

	return cmd
}

// reason 创建一个句子片段，以便锁定原因可用于句子中。
//
// 例如 "resolved" -> "(原因: RESOLVED)"
func reason(reason string) string {
	result := ""
	if reason != "" {
		result = fmt.Sprintf("(原因: %s)", strings.ToUpper(reason))
	}
	return result
}

// status 创建一个字符串，显示成功锁定/解除锁定的结果，
// 该结果由一系列选项参数化。
//
// 示例输出: "已锁定(原因: RESOLVED): 议题 #31 (议题标题)"
func status(state string, lockable *api.Issue, baseRepo ghrepo.Interface, opts *LockOptions) string {
	action := "已锁定"
	if state == Unlock {
		action = "已解除锁定"
	}
	return fmt.Sprintf("%s%s: %s %s#%d (%s)",
		action, reason(opts.Reason), alias[opts.ParentCmd].FullName, ghrepo.FullName(baseRepo), lockable.Number, lockable.Title)
}

// lockRun 将锁定或解除锁定对话。
func lockRun(state string, opts *LockOptions) error {
	cs := opts.IO.ColorScheme()

	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	issuePr, err := issueShared.FindIssueOrPR(httpClient, baseRepo, opts.IssueNumber, fields())
	if err != nil {
		return err
	}

	parent := alias[opts.ParentCmd]
	if parent.Typename != issuePr.Typename {
		currentType := alias[parent.Typename]
		correctType := alias[issuePr.Typename]

		return fmt.Errorf("%s %s %s#%d 未找到，但找到了 %s %s#%d。请改用 `gh %s %s %d`",
			cs.FailureIconWithColor(cs.Red),
			currentType.FullName, ghrepo.FullName(baseRepo), issuePr.Number,
			strings.ToLower(correctType.FullName), ghrepo.FullName(baseRepo), issuePr.Number,
			correctType.Name, strings.ToLower(state), issuePr.Number)
	}

	if opts.Interactive {
		options := []string{"无", "离题", "已解决", "垃圾信息", "过于激烈"}
		selected, err := opts.Prompter.Select("锁定原因?", "", options)
		if err != nil {
			return err
		}
		if selected > 0 {
			opts.Reason = reasons[selected-1]
		}
	}

	successMsg := fmt.Sprintf("%s %s\n",
		cs.SuccessIconWithColor(cs.Green), status(state, issuePr, baseRepo, opts))

	switch state {
	case Lock:
		if !issuePr.Locked {
			err = lockLockable(httpClient, baseRepo, issuePr, opts)
		} else {
			var relocked bool
			relocked, err = relockLockable(httpClient, baseRepo, issuePr, opts)

			if !relocked {
				successMsg = fmt.Sprintf("%s %s#%d 已锁定%s。未做任何更改。\n",
					parent.FullName, ghrepo.FullName(baseRepo), issuePr.Number, reason(issuePr.ActiveLockReason))
			}
		}

	case Unlock:
		if issuePr.Locked {
			err = unlockLockable(httpClient, baseRepo, issuePr)
		} else {
			successMsg = fmt.Sprintf("%s %s#%d 已解除锁定。未做任何更改。\n",
				parent.FullName, ghrepo.FullName(baseRepo), issuePr.Number)
		}
	default:
		panic("bad state")
	}

	if err != nil {
		return err
	}

	if opts.IO.IsStdoutTTY() {
		fmt.Fprint(opts.IO.Out, successMsg)
	}

	return nil
}

// lockLockable 将锁定议题或拉取请求
func lockLockable(httpClient *http.Client, repo ghrepo.Interface, lockable *api.Issue, opts *LockOptions) error {
	var mutation struct {
		LockLockable struct {
			LockedRecord struct {
				Locked bool
			}
		} `graphql:"lockLockable(input: $input)"`
	}

	variables := map[string]any{
		"input": githubv4.LockLockableInput{
			LockableID: lockable.ID,
			LockReason: reasonsMap[opts.Reason],
		},
	}

	gql := api.NewClientFromHTTP(httpClient)
	return gql.Mutate(repo.RepoHost(), "LockLockable", &mutation, variables)
}

// unlockLockable 将解除锁定议题或拉取请求
func unlockLockable(httpClient *http.Client, repo ghrepo.Interface, lockable *api.Issue) error {

	var mutation struct {
		UnlockLockable struct {
			UnlockedRecord struct {
				Locked bool
			}
		} `graphql:"unlockLockable(input: $input)"`
	}

	variables := map[string]any{
		"input": githubv4.UnlockLockableInput{
			LockableID: lockable.ID,
		},
	}

	gql := api.NewClientFromHTTP(httpClient)
	return gql.Mutate(repo.RepoHost(), "UnlockLockable", &mutation, variables)
}

// relockLockable 先解除锁定再锁定议题或拉取请求。一个常见用例
// 是更改锁定的原因。
//
// 当前的 api 不允许通过单个锁定请求更新已锁定的项；
// 它会直接忽略该请求。你需要先解除锁定，再以新的原因锁定。
func relockLockable(httpClient *http.Client, repo ghrepo.Interface, lockable *api.Issue, opts *LockOptions) (bool, error) {
	if !opts.IO.CanPrompt() {
		return false, errors.New("已锁定")
	}

	prompt := fmt.Sprintf("%s %s#%d 已锁定%s。是否解除锁定并重新锁定%s?",
		alias[opts.ParentCmd].FullName, ghrepo.FullName(repo), lockable.Number, reason(lockable.ActiveLockReason), reason(opts.Reason))

	relocked, err := opts.Prompter.Confirm(prompt, true)
	if err != nil {
		return false, err
	} else if !relocked {
		return relocked, nil
	}

	err = unlockLockable(httpClient, repo, lockable)
	if err != nil {
		return relocked, err
	}

	err = lockLockable(httpClient, repo, lockable, opts)
	if err != nil {
		return relocked, err
	}

	return relocked, nil
}
