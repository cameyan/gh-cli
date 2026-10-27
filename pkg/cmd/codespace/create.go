package codespace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/cli/cli/v2/internal/codespaces"
	"github.com/cli/cli/v2/internal/codespaces/api"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

const (
	DEVCONTAINER_PROMPT_DEFAULT = "默认 Codespaces 配置"
)

const (
	permissionsPollingInterval = 5 * time.Second
	permissionsPollingTimeout  = 1 * time.Minute
)

const (
	displayNameMaxLength = 48 // 48 是 API 中显示名称的最大长度
)

var (
	DEFAULT_DEVCONTAINER_DEFINITIONS = []string{".devcontainer.json", ".devcontainer/devcontainer.json"}
)

type NullableDuration struct {
	*time.Duration
}

func (d *NullableDuration) String() string {
	if d.Duration != nil {
		return d.Duration.String()
	}

	return ""
}

func (d *NullableDuration) Set(str string) error {
	duration, err := time.ParseDuration(str)
	if err != nil {
		return fmt.Errorf("解析时长出错: %w", err)
	}
	d.Duration = &duration
	return nil
}

func (d *NullableDuration) Type() string {
	return "duration"
}

func (d *NullableDuration) Minutes() *int {
	if d.Duration != nil {
		retentionMinutes := int(d.Duration.Minutes())
		return &retentionMinutes
	}

	return nil
}

type createOptions struct {
	repo              string
	branch            string
	location          string
	machine           string
	showStatus        bool
	permissionsOptOut bool
	devContainerPath  string
	idleTimeout       time.Duration
	retentionPeriod   NullableDuration
	displayName       string
	useWeb            bool
}

func newCreateCmd(app *App) *cobra.Command {
	opts := createOptions{}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "创建 codespace",
		Args:  noArgsConstraint,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if opts.repo != "" {
				if err := validateNWO(opts.repo); err != nil {
					return cmdutil.FlagErrorf("--repo 的值无效: %v", err)
				}
			}
			return cmdutil.MutuallyExclusive(
				"--web 不能与 --display-name、--idle-timeout 或 --retention-period 同时使用",
				opts.useWeb,
				opts.displayName != "" || opts.idleTimeout != 0 || opts.retentionPeriod.Duration != nil,
			)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.Create(cmd.Context(), opts)
		},
	}

	createCmd.Flags().BoolVarP(&opts.useWeb, "web", "w", false, "从浏览器创建 codespace，不能与 --display-name、--idle-timeout 或 --retention-period 同时使用")

	createCmd.Flags().StringVarP(&opts.repo, "repo", "R", "", "带所有者的仓库名称: user/repo")
	if err := addDeprecatedRepoShorthand(createCmd, &opts.repo); err != nil {
		fmt.Fprintf(app.io.ErrOut, "%v\n", err)
	}

	createCmd.Flags().StringVarP(&opts.branch, "branch", "b", "", "仓库分支")
	createCmd.Flags().StringVarP(&opts.location, "location", "l", "", "位置: {EastUs|SouthEastAsia|WestEurope|WestUs2} (未提供时自动确定)")
	createCmd.Flags().StringVarP(&opts.machine, "machine", "m", "", "虚拟机的硬件规格")
	createCmd.Flags().BoolVarP(&opts.permissionsOptOut, "default-permissions", "", false, "不提示接受 codespace 请求的额外权限")
	createCmd.Flags().BoolVarP(&opts.showStatus, "status", "s", false, "显示创建后命令和 dotfiles 的状态")
	createCmd.Flags().DurationVar(&opts.idleTimeout, "idle-timeout", 0, "codespace 停止前允许的闲置时间，如 \"10m\"、\"1h\"")
	createCmd.Flags().Var(&opts.retentionPeriod, "retention-period", "codespace 关闭后到被自动删除前允许保留的时间 (最长 30 天)，如 \"1h\"、\"72h\"")
	createCmd.Flags().StringVar(&opts.devContainerPath, "devcontainer-path", "", "创建 codespace 时使用的 devcontainer.json 文件路径")
	createCmd.Flags().StringVarP(&opts.displayName, "display-name", "d", "", fmt.Sprintf("codespace 的显示名称 (不超过 %d 个字符)", displayNameMaxLength))

	return createCmd
}

// Create 创建一个新的 codespace
func (a *App) Create(ctx context.Context, opts createOptions) error {
	// Overrides for Codespace developers to target test environments
	vscsLocation := os.Getenv("VSCS_LOCATION")
	vscsTarget := os.Getenv("VSCS_TARGET")
	vscsTargetUrl := os.Getenv("VSCS_TARGET_URL")

	userInputs := struct {
		Repository string
		Branch     string
		Location   string
	}{
		Repository: opts.repo,
		Branch:     opts.branch,
		Location:   opts.location,
	}

	if opts.useWeb && userInputs.Repository == "" {
		return a.browser.Browse(fmt.Sprintf("%s/codespaces/new", a.apiClient.ServerURL()))
	}

	prompter := &Prompter{}
	promptForRepoAndBranch := userInputs.Repository == "" && !opts.useWeb
	if promptForRepoAndBranch {
		var defaultRepo string
		if remotes, _ := a.remotes(); remotes != nil {
			if defaultRemote, _ := remotes.ResolvedRemote(); defaultRemote != nil {
				// this is a remote explicitly chosen via `repo set-default`
				defaultRepo = ghrepo.FullName(defaultRemote)
			} else if len(remotes) > 0 {
				// as a fallback, just pick the first remote
				// 兜底方案: 选择第一个远程
				defaultRepo = ghrepo.FullName(remotes[0])
			}
		}

		repoQuestions := []*survey.Question{
			{
				Name: "repository",
				Prompt: &survey.Input{
					Message: "仓库:",
					Help:    "按名称搜索仓库。要在组织或用户内搜索，或查看私有仓库，请输入至少 ':user/'。",
					Default: defaultRepo,
					Suggest: func(toComplete string) []string {
						return getRepoSuggestions(ctx, a.apiClient, toComplete)
					},
				},
				Validate: survey.Required,
			},
		}
		if err := prompter.Ask(repoQuestions, &userInputs); err != nil {
			return fmt.Errorf("提示输入失败: %w", err)
		}
	}

	if userInputs.Location == "" && vscsLocation != "" {
		userInputs.Location = vscsLocation
	}

	var repository *api.Repository
	err := a.RunWithProgress("正在获取仓库", func() (err error) {
		repository, err = a.apiClient.GetRepository(ctx, userInputs.Repository)
		return
	})
	if err != nil {
		return fmt.Errorf("获取仓库时出错: %w", err)
	}

	var billableOwner *api.User
	err = a.RunWithProgress("正在校验仓库是否支持 codespaces", func() (err error) {
		billableOwner, err = a.apiClient.GetCodespaceBillableOwner(ctx, userInputs.Repository)
		return
	})
	if err != nil {
		return fmt.Errorf("检查 codespace 归属时出错: %w", err)
	} else if billableOwner != nil && (billableOwner.Type == "Organization" || billableOwner.Type == "User") {
		cs := a.io.ColorScheme()
		fmt.Fprintln(a.io.ErrOut, cs.Blue("  ✓ 该仓库的 Codespaces 用量由 "+billableOwner.Login+" 付费"))
	}

	if promptForRepoAndBranch {
		branchPrompt := "分支 (留空使用默认分支):"
		if userInputs.Branch != "" {
			branchPrompt = "分支:"
		}
		branchQuestions := []*survey.Question{
			{
				Name: "branch",
				Prompt: &survey.Input{
					Message: branchPrompt,
					Default: userInputs.Branch,
				},
			},
		}

		if err := prompter.Ask(branchQuestions, &userInputs); err != nil {
			return fmt.Errorf("提示输入失败: %w", err)
		}
	}

	branch := userInputs.Branch
	if branch == "" {
		branch = repository.DefaultBranch
	}

	devContainerPath := opts.devContainerPath

	// now that we have repo+branch, we can list available devcontainer.json files (if any)
	if opts.devContainerPath == "" {
		var devcontainers []api.DevContainerEntry
		err = a.RunWithProgress("正在获取 devcontainer.json 文件", func() (err error) {
			devcontainers, err = a.apiClient.ListDevContainers(ctx, repository.ID, branch, 100)
			return
		})
		if err != nil {
			return fmt.Errorf("获取 devcontainer.json 路径时出错: %w", err)
		}

		if len(devcontainers) > 0 {

			// if there is only one devcontainer.json file and it is one of the default paths we can auto-select it
			// 若只有一个 devcontainer.json 文件且属于默认路径之一，可以自动选中它
			if len(devcontainers) == 1 && slices.Contains(DEFAULT_DEVCONTAINER_DEFINITIONS, devcontainers[0].Path) {
				devContainerPath = devcontainers[0].Path
			} else {
				promptOptions := []string{}

				if !slices.Contains(DEFAULT_DEVCONTAINER_DEFINITIONS, devcontainers[0].Path) {
					promptOptions = []string{DEVCONTAINER_PROMPT_DEFAULT}
				}

				for _, devcontainer := range devcontainers {
					promptOptions = append(promptOptions, devcontainer.Path)
				}

				devContainerPathQuestion := &survey.Question{
					Name: "devContainerPath",
					Prompt: &survey.Select{
						Message: "Devcontainer 定义文件:",
						Options: promptOptions,
					},
				}

				if err := prompter.Ask([]*survey.Question{devContainerPathQuestion}, &devContainerPath); err != nil {
					return fmt.Errorf("提示输入失败: %w", err)
				}
			}
		}

		if devContainerPath == DEVCONTAINER_PROMPT_DEFAULT {
			// special arg allows users to opt out of devcontainer.json selection
			devContainerPath = ""
		}
	}

	machine := opts.machine
	// skip this if we have useWeb and no machine name provided,
	// because web UI will select default machine type if none is provided
	// web UI also provide a way to select machine type
	// therefore we let the user choose from the web UI instead of prompting from CLI
	// 若使用 --web 且未提供机器名则跳过此步，
	// 因为未提供时网页界面会选择默认机器类型，
	// 网页界面也提供选择机器类型的方式，
	// 因此让用户在网页界面选择，而不是由 CLI 提示。
	if !(opts.useWeb && opts.machine == "") {
		machine, err = getMachineName(ctx, a.apiClient, prompter, repository.ID, opts.machine, branch, userInputs.Location, devContainerPath)
		if err != nil {
			return fmt.Errorf("获取机器类型时出错: %w", err)
		}
		if machine == "" {
			return errors.New("该仓库没有可用的机器类型")
		}
	}

	if len(opts.displayName) > displayNameMaxLength {
		return fmt.Errorf("创建 codespace 出错: 显示名称最多包含 %d 个字符", displayNameMaxLength)
	}

	createParams := &api.CreateCodespaceParams{
		RepositoryID:           repository.ID,
		Branch:                 branch,
		Machine:                machine,
		Location:               userInputs.Location,
		VSCSTarget:             vscsTarget,
		VSCSTargetURL:          vscsTargetUrl,
		IdleTimeoutMinutes:     int(opts.idleTimeout.Minutes()),
		RetentionPeriodMinutes: opts.retentionPeriod.Minutes(),
		DevContainerPath:       devContainerPath,
		PermissionsOptOut:      opts.permissionsOptOut,
		DisplayName:            opts.displayName,
	}

	if opts.useWeb {
		return a.browser.Browse(fmt.Sprintf("%s/codespaces/new?repo=%d&ref=%s&machine=%s&location=%s", a.apiClient.ServerURL(), createParams.RepositoryID, createParams.Branch, createParams.Machine, createParams.Location))
	}

	var codespace *api.Codespace
	err = a.RunWithProgress("正在创建 codespace", func() (err error) {
		codespace, err = a.apiClient.CreateCodespace(ctx, createParams)
		return
	})

	if err != nil {
		var aerr api.AcceptPermissionsRequiredError
		if !errors.As(err, &aerr) || aerr.AllowPermissionsURL == "" {
			return fmt.Errorf("error creating codespace: %w", err)
		}

		codespace, err = a.handleAdditionalPermissions(ctx, prompter, createParams, aerr.AllowPermissionsURL)
		if err != nil {
			// this error could be a cmdutil.SilentError (in the case that the user opened the browser) so we don't want to wrap it
			return err
		}
	}

	if opts.showStatus {
		if err := a.showStatus(ctx, codespace); err != nil {
			return fmt.Errorf("显示状态失败: %w", err)
		}
	}

	cs := a.io.ColorScheme()

	fmt.Fprintln(a.io.Out, codespace.Name)

	if a.io.IsStderrTTY() && codespace.IdleTimeoutNotice != "" {
		fmt.Fprintln(a.io.ErrOut, cs.Yellow("提示:"), codespace.IdleTimeoutNotice)
	}

	return nil
}

func (a *App) handleAdditionalPermissions(ctx context.Context, prompter SurveyPrompter, createParams *api.CreateCodespaceParams, allowPermissionsURL string) (*api.Codespace, error) {
	var (
		isInteractive = a.io.CanPrompt()
		cs            = a.io.ColorScheme()
	)

	fmt.Fprintf(a.io.ErrOut, "继续之前必须授权或拒绝该 codespace 请求的额外权限。\n")

	if !isInteractive {
		fmt.Fprintf(a.io.ErrOut, "在浏览器中%s以检查并授权额外权限: %s\n", cs.Bold("打开此 URL"), allowPermissionsURL)
		fmt.Fprintf(a.io.ErrOut, "也可以运行 %q 并加上 %q 选项，在不授权额外权限的情况下继续。\n", a.io.ColorScheme().Bold("create"), cs.Bold("--default-permissions"))
		return nil, cmdutil.SilentError
	}

	choices := []string{
		"在浏览器中继续，检查并授权额外权限 (推荐)",
		"不授权额外权限，继续创建",
	}

	permsSurvey := []*survey.Question{
		{
			Name: "accept",
			Prompt: &survey.Select{
				Message: "你想怎么做？",
				Options: choices,
				Default: choices[0],
			},
			Validate: survey.Required,
		},
	}

	var answers struct {
		Accept string
	}

	if err := prompter.Ask(permsSurvey, &answers); err != nil {
		return nil, fmt.Errorf("获取回答时出错: %w", err)
	}

	// if the user chose to continue in the browser, open the URL
	// 若用户选择在浏览器中继续，则打开该 URL
	if answers.Accept == choices[0] {
		if err := a.browser.Browse(allowPermissionsURL); err != nil {
			return nil, fmt.Errorf("打开浏览器时出错: %w", err)
		}

		// Poll until the user has accepted the permissions or timeout
		// 轮询直到用户接受权限或超时
		if err := a.pollForPermissions(ctx, createParams); err != nil {
			return nil, fmt.Errorf("轮询权限时出错: %w", err)
		}
	} else {
		// If the user chose to create the codespace without the permissions,
		// we can continue with the create opting out of the additional permissions
		// 若用户选择不授权权限创建 codespace，
		// 则跳过额外权限继续创建
		createParams.PermissionsOptOut = true
	}

	var codespace *api.Codespace
	err := a.RunWithProgress("正在创建 codespace", func() (err error) {
		codespace, err = a.apiClient.CreateCodespace(ctx, createParams)
		return
	})

	if err != nil {
		return nil, fmt.Errorf("创建 codespace 时出错: %w", err)
	}

	return codespace, nil
}

func (a *App) pollForPermissions(ctx context.Context, createParams *api.CreateCodespaceParams) error {
	return a.RunWithProgress("等待在浏览器中接受权限", func() (err error) {
		ctx, cancel := context.WithTimeout(ctx, permissionsPollingTimeout)
		defer cancel()

		done := make(chan error, 1)
		go func() {
			for {
				accepted, err := a.apiClient.GetCodespacesPermissionsCheck(ctx, createParams.RepositoryID, createParams.Branch, createParams.DevContainerPath)
				if err != nil {
					done <- err
					return
				}

				if accepted {
					done <- nil
					return
				}

				// Wait before polling again
				// 等待后再次轮询
				time.Sleep(permissionsPollingInterval)
			}
		}()

		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return fmt.Errorf("等待在浏览器中接受权限超时")
		}
	})
}

// showStatus 轮询 codespace 的创建后状态及其状态信息，持续轮询直到所有状态完成。
// 所有状态完成后，再轮询一次检查是否有新状态出现，否则停止轮询。
func (a *App) showStatus(ctx context.Context, codespace *api.Codespace) error {
	var (
		lastState      codespaces.PostCreateState
		breakNextState bool
	)

	finishedStates := make(map[string]bool)
	ctx, stopPolling := context.WithCancel(ctx)
	defer stopPolling()

	poller := func(states []codespaces.PostCreateState) {
		var inProgress bool
		for _, state := range states {
			if _, found := finishedStates[state.Name]; found {
				continue // 跳过已处理过的状态
			}

			if state.Name != lastState.Name {
				a.StartProgressIndicatorWithLabel(state.Name)

				if state.Status == codespaces.PostCreateStateRunning {
					inProgress = true
					lastState = state
					break
				}

				finishedStates[state.Name] = true
				a.StopProgressIndicator()
			} else {
				if state.Status == codespaces.PostCreateStateRunning {
					inProgress = true
					break
				}

				finishedStates[state.Name] = true
				a.StopProgressIndicator()
				lastState = codespaces.PostCreateState{} // 重置该值
			}
		}

		if !inProgress {
			if breakNextState {
				stopPolling()
				return
			}
			breakNextState = true
		}
	}

	err := codespaces.PollPostCreateStates(ctx, a, a.apiClient, codespace, poller)
	if err != nil {
		if errors.Is(err, context.Canceled) && breakNextState {
			return nil // 我们通过取消 context 来停止轮询，可以忽略该错误
		}

		return fmt.Errorf("轮询 codespace 状态变更失败: %w", err)
	}

	return nil
}

// getMachineName 提示用户选择机器类型，machine 非空时校验该机器。
func getMachineName(ctx context.Context, apiClient apiClient, prompter SurveyPrompter, repoID int64, machine, branch, location string, devcontainerPath string) (string, error) {
	machines, err := apiClient.GetCodespacesMachines(ctx, repoID, branch, location, devcontainerPath)
	if err != nil {
		return "", fmt.Errorf("请求机器实例类型时出错: %w", err)
	}

	// if user supplied a machine type, it must be valid
	// if no machine type was supplied, we don't error if there are no machine types for the current repo
	// 若用户提供了机器类型，则必须有效
	// 若未提供机器类型，则当前仓库没有机器类型时不报错
	if machine != "" {
		for _, m := range machines {
			if machine == m.Name {
				return machine, nil
			}
		}

		availableMachines := make([]string, len(machines))
		for i := range machines {
			availableMachines[i] = machines[i].Name
		}

		return "", fmt.Errorf("该仓库没有这样的机器: %s\n可用机器: %v", machine, availableMachines)
	} else if len(machines) == 0 {
		return "", nil
	}

	if len(machines) == 1 {
		// VS Code does not prompt for machine if there is only one, this makes us consistent with that behavior
		// 只有一个机器时 VS Code 不提示选择，我们与此行为保持一致
		return machines[0].Name, nil
	}

	machineNames := make([]string, 0, len(machines))
	machineByName := make(map[string]*api.Machine)
	for _, m := range machines {
		machineName := buildDisplayName(m.DisplayName, m.PrebuildAvailability)
		machineNames = append(machineNames, machineName)
		machineByName[machineName] = m
	}

	machineSurvey := []*survey.Question{
		{
			Name: "machine",
			Prompt: &survey.Select{
				Message: "选择机器类型:",
				Options: machineNames,
				Default: machineNames[0],
			},
			Validate: survey.Required,
		},
	}

	var machineAnswers struct{ Machine string }
	if err := prompter.Ask(machineSurvey, &machineAnswers); err != nil {
		return "", fmt.Errorf("获取机器时出错: %w", err)
	}

	selectedMachine := machineByName[machineAnswers.Machine]

	return selectedMachine.Name, nil
}

func getRepoSuggestions(ctx context.Context, apiClient apiClient, partialSearch string) []string {
	searchParams := api.RepoSearchParameters{
		// The prompt shows 7 items so 7 effectively turns off scrolling which is similar behavior to other clients
		// 提示界面显示 7 项，因此 7 实际上关闭了滚动，与其他客户端行为一致
		MaxRepos: 7,
		Sort:     "repo",
	}

	repos, err := apiClient.GetCodespaceRepoSuggestions(ctx, partialSearch, searchParams)
	if err != nil {
		return nil
	}

	return repos
}

// buildDisplayName 返回用于机器选择提示的显示名称。
// prebuildAvailability 将在 Prebuild GA 前迁移为枚举值: "none"、"ready"、"in_progress"
func buildDisplayName(displayName string, prebuildAvailability string) string {
	switch prebuildAvailability {
	case "ready":
		return displayName + " (预构建就绪)"
	case "in_progress":
		return displayName + " (预构建进行中)"
	default:
		return displayName
	}
}
