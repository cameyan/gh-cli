package codespace

// 本文件定义整个 codespace 命令集共用的函数。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/terminal"
	clicontext "github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/internal/safeurl"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type executable interface {
	Executable() string
}

type App struct {
	io         *iostreams.IOStreams
	apiClient  apiClient
	errLogger  *log.Logger
	executable executable
	browser    browser.Browser
	remotes    func() (clicontext.Remotes, error)
}

func NewApp(io *iostreams.IOStreams, exe executable, apiClient apiClient, browser browser.Browser, remotes func() (clicontext.Remotes, error)) *App {
	errLogger := log.New(io.ErrOut, "", 0)

	return &App{
		io:         io,
		apiClient:  apiClient,
		errLogger:  errLogger,
		executable: exe,
		browser:    browser,
		remotes:    remotes,
	}
}

// StartProgressIndicatorWithLabel 启动一个带消息的进度指示器。
func (a *App) StartProgressIndicatorWithLabel(s string) {
	a.io.StartProgressIndicatorWithLabel(s)
}

// StopProgressIndicator 停止进度指示器。
func (a *App) StopProgressIndicator() {
	a.io.StopProgressIndicator()
}

func (a *App) RunWithProgress(label string, run func() error) error {
	return a.io.RunWithProgress(label, run)
}

//go:generate moq -fmt goimports -rm -skip-ensure -out mock_api.go . apiClient
type apiClient interface {
	ServerURL() string
	GetUser(ctx context.Context) (*api.User, error)
	GetCodespace(ctx context.Context, name string, includeConnection bool) (*api.Codespace, error)
	GetOrgMemberCodespace(ctx context.Context, orgName string, userName string, codespaceName string) (*api.Codespace, error)
	ListCodespaces(ctx context.Context, opts api.ListCodespacesOptions) ([]*api.Codespace, error)
	DeleteCodespace(ctx context.Context, name string, orgName string, userName string) error
	StartCodespace(ctx context.Context, name string) error
	StopCodespace(ctx context.Context, name string, orgName string, userName string) error
	CreateCodespace(ctx context.Context, params *api.CreateCodespaceParams) (*api.Codespace, error)
	EditCodespace(ctx context.Context, codespaceName string, params *api.EditCodespaceParams) (*api.Codespace, error)
	GetRepository(ctx context.Context, nwo string) (*api.Repository, error)
	GetCodespacesMachines(ctx context.Context, repoID int64, branch string, location string, devcontainerPath string) ([]*api.Machine, error)
	GetCodespacesPermissionsCheck(ctx context.Context, repoID int64, branch string, devcontainerPath string) (bool, error)
	GetCodespaceRepositoryContents(ctx context.Context, codespace *api.Codespace, path string) ([]byte, error)
	ListDevContainers(ctx context.Context, repoID int64, branch string, limit int) (devcontainers []api.DevContainerEntry, err error)
	GetCodespaceRepoSuggestions(ctx context.Context, partialSearch string, params api.RepoSearchParameters) ([]string, error)
	GetCodespaceBillableOwner(ctx context.Context, nwo string) (*api.User, error)
	ExternalHTTPClient() (*http.Client, error)
}

var errNoCodespaces = errors.New("你没有任何 codespace")

// chooseCodespaceFromList 返回用户从列表中交互选择的 codespace，
// 没有任何 codespace 时返回错误。
func chooseCodespaceFromList(ctx context.Context, codespaces []*api.Codespace, includeOwner bool, skipPromptForSingleOption bool) (*api.Codespace, error) {
	if len(codespaces) == 0 {
		return nil, errNoCodespaces
	}

	if skipPromptForSingleOption && len(codespaces) == 1 {
		return codespaces[0], nil
	}

	sortedCodespaces := codespaces
	sort.Slice(sortedCodespaces, func(i, j int) bool {
		return sortedCodespaces[i].CreatedAt > sortedCodespaces[j].CreatedAt
	})

	csSurvey := []*survey.Question{
		{
			Name: "codespace",
			Prompt: &survey.Select{
				Message: "选择 codespace:",
				Options: formatCodespacesForSelect(sortedCodespaces, includeOwner),
			},
			Validate: survey.Required,
		},
	}

	prompter := &Prompter{}
	var answers struct {
		Codespace int
	}
	if err := prompter.Ask(csSurvey, &answers); err != nil {
		return nil, fmt.Errorf("获取回答时出错: %w", err)
	}

	return sortedCodespaces[answers.Codespace], nil
}

func formatCodespacesForSelect(codespaces []*api.Codespace, includeOwner bool) []string {
	names := make([]string, len(codespaces))

	for i, apiCodespace := range codespaces {
		cs := codespace{apiCodespace}
		names[i] = cs.displayName(includeOwner)
	}

	return names
}

func safeClose(closer io.Closer, err *error) {
	if closeErr := closer.Close(); *err == nil {
		*err = closeErr
	}
}

// hasTTY 指示进程是否连接到终端。
// 假设 stdin/stdout 是文件描述符 0 和 1 是不可移植的。
var hasTTY = term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))

type SurveyPrompter interface {
	Ask(qs []*survey.Question, response any) error
}

type Prompter struct{}

// ask 在终端上以标准选项显示 survey 问题。
// 除非 hasTTY 为真，否则会失败，但理想情况下调用方应避免在该情况下调用。
func (p *Prompter) Ask(qs []*survey.Question, response any) error {
	if !hasTTY {
		return fmt.Errorf("无终端")
	}
	err := survey.Ask(qs, response, survey.WithShowCursor(true))
	// The survey package temporarily clears the terminal's ISIG mode bit
	// (see tcsetattr(3)) so the QUIT button (Ctrl-C) is reported as
	// ASCII \x03 (ETX) instead of delivering SIGINT to the application.
	// So we have to serve ourselves the SIGINT.
	// survey 包会临时清除终端的 ISIG 模式位 (见 tcsetattr(3))，
	// 使 QUIT 键 (Ctrl-C) 以 ASCII \x03 (ETX) 上报，而不是向应用递送 SIGINT。
	// 因此我们必须自己触发 SIGINT。
	//
	// https://github.com/AlecAivazis/survey/#why-isnt-ctrl-c-working
	if err == terminal.InterruptErr {
		self, _ := os.FindProcess(os.Getpid())
		_ = self.Signal(os.Interrupt) // assumes POSIX

		// Suspend the goroutine, to avoid a race between
		// return from main and async delivery of INT signal.
		// 挂起该 goroutine，避免从 main 返回与 INT 信号异步递送之间产生竞争。
		select {}
	}
	return err
}

var ErrTooManyArgs = errors.New("该命令不接受任何参数")

func noArgsConstraint(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return ErrTooManyArgs
	}
	return nil
}

type codespace struct {
	*api.Codespace
}

// displayName 为交互选择器提示格式化 codespace 名称。
func (c codespace) displayName(includeOwner bool) string {
	branch := c.branchWithGitStatus()
	displayName := c.DisplayName

	if displayName == "" {
		displayName = c.Name
	}

	description := fmt.Sprintf("%s [%s]: %s", c.Repository.FullName, branch, displayName)

	if includeOwner {
		description = fmt.Sprintf("%-15s %s", c.Owner.Login, description)
	}

	return description
}

// gitStatusDirty 表示有未保存的更改状态。
const gitStatusDirty = "*"

// branchWithGitStatus 在分支名后附加星号，
// 表示该分支当前有正在进行的修改。
func (c codespace) branchWithGitStatus() string {
	if c.hasUnsavedChanges() {
		return c.GitStatus.Ref + gitStatusDirty
	}

	return c.GitStatus.Ref
}

// hasUnsavedChanges 返回环境中是否有未保存的更改。
func (c codespace) hasUnsavedChanges() bool {
	return c.GitStatus.HasUncommittedChanges || c.GitStatus.HasUnpushedChanges
}

// running 返回 codespace 环境是否正在运行。
func (c codespace) running() bool {
	return c.State == api.CodespaceStateAvailable
}

// addDeprecatedRepoShorthand 添加 -r 参数 (--repo 的已弃用简写)，
// 提示用户改用 -R。
func addDeprecatedRepoShorthand(cmd *cobra.Command, target *string) error {
	cmd.Flags().StringVarP(target, "repo-deprecated", "r", "", "(已弃用) --repo 的简写")

	if err := cmd.Flags().MarkHidden("repo-deprecated"); err != nil {
		return fmt.Errorf("将 `-r` 简写标记为隐藏时出错: %w", err)
	}

	if err := cmd.Flags().MarkShorthandDeprecated("repo-deprecated", "请改用 `-R`"); err != nil {
		return fmt.Errorf("将 `-r` 简写标记为已弃用时出错: %w", err)
	}

	if cmd.Flag("codespace") != nil {
		cmd.MarkFlagsMutuallyExclusive("codespace", "repo-deprecated")
	}

	return nil
}

// validateNWO 在 nwo 不是有效的 "owner/repo" 仓库引用时返回错误。
func validateNWO(nwo string) error {
	_, _, err := safeurl.RepoPartsFromNWO(nwo)
	return err
}

// filterCodespacesByRepoOwner 按仓库 owner 过滤 codespace 列表。
func filterCodespacesByRepoOwner(codespaces []*api.Codespace, repoOwner string) []*api.Codespace {
	filtered := make([]*api.Codespace, 0, len(codespaces))
	for _, c := range codespaces {
		if strings.EqualFold(c.Repository.Owner.Login, repoOwner) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}
