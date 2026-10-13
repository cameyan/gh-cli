package shared

import (
	"errors"

	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/pkg/cmd/auth/shared/gitcredentials"
)

type HelperConfig interface {
	ConfigureOurs(hostname string) error
	ConfiguredHelper(hostname string) (gitcredentials.Helper, error)
}

type GitCredentialFlow struct {
	Prompter Prompt

	HelperConfig HelperConfig
	Updater      *gitcredentials.Updater

	shouldSetup bool
	helper      gitcredentials.Helper
	scopes      []string
}

func (flow *GitCredentialFlow) Prompt(hostname string) error {
	// 首先获取将用于此主机的凭据助手(credential helper)
	var configuredHelperErr error
	flow.helper, configuredHelperErr = flow.HelperConfig.ConfiguredHelper(hostname)
	// 如果助手就是 gh 本身，则无需询问用户是否要更新其 git 凭据，
	// 因为 gh 会返回活跃令牌，这一步会自动完成。
	//
	// 由于助手是 gh，此令牌可能被用于 git 操作，因此我们将额外请求 workflow
	// 作用域(scope)，以确保包含 workflow 变更的 git push 操作能够成功。
	if flow.helper.IsOurs() {
		flow.scopes = append(flow.scopes, "workflow")
		return nil
	}

	// 询问用户是否要使用新获取的令牌配置 git
	result, err := flow.Prompter.Confirm("使用您的 GitHub 凭据对 Git 进行身份验证？", true)
	if err != nil {
		return err
	}
	flow.shouldSetup = result

	if flow.shouldSetup {
		// 如果用户确实想配置 git，我们将检查上面获取已配置助手时返回的错误。
		// 如果错误表明未安装 git，我们现在就返回该错误，以确保在用户继续操作之前
		// 中止身份验证流程。
		//
		// 请注意，这_略微_有些天真，因为获取已配置助手失败可能还有其它原因，
		// 这些原因可能导致后续失败，但这段代码已存在很长时间了，
		// 我不希望在重构中更改它。
		//
		// 参考:
		//  * https://git-scm.com/docs/git-config#_description
		//  * https://github.com/cli/cli/pull/4109
		if _, ok := errors.AsType[*git.NotInstalled](configuredHelperErr); ok {
			return configuredHelperErr
		}

		// 另一方面，如果用户请求了配置，我们将额外请求 workflow
		// 作用域(scope)，以确保包含 workflow 变更的 git push 操作能够成功。
		flow.scopes = append(flow.scopes, "workflow")
	}

	return nil
}

func (flow *GitCredentialFlow) Scopes() []string {
	return flow.scopes
}

func (flow *GitCredentialFlow) ShouldSetup() bool {
	return flow.shouldSetup
}

func (flow *GitCredentialFlow) Setup(hostname, username, authToken string) error {
	// 如果没有配置任何凭据助手，则我们将自己设置为
	// 此主机的凭据助手。
	if !flow.helper.IsConfigured() {
		return flow.HelperConfig.ConfigureOurs(hostname)
	}

	// 否则，我们将告知 git 把新凭据通知现有的凭据助手。
	return flow.Updater.Update(hostname, username, authToken)
}
