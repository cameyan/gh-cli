package contract

import (
	"testing"

	"github.com/cli/cli/v2/pkg/cmd/auth/shared"
	"github.com/stretchr/testify/require"
)

// This HelperConfig contract exist to ensure that any HelperConfig implementation conforms to this behaviour.
// This is useful because we can swap in fake implementations for testing, rather than requiring our tests to be
// isolated from git.
//
// See for example, TestAuthenticatingGitCredentials for LoginFlow.
type HelperConfig struct {
	NewHelperConfig func(t *testing.T) shared.HelperConfig
	ConfigureHelper func(t *testing.T, hostname string)
}

func (contract HelperConfig) Test(t *testing.T) {
	t.Run("当没有凭证助手时，为仓库和代码片段主机配置 gh", func(t *testing.T) {
		hc := contract.NewHelperConfig(t)
		require.NoError(t, hc.ConfigureOurs("github.com"))

		repoHelper, err := hc.ConfiguredHelper("github.com")
		require.NoError(t, err)
		require.True(t, repoHelper.IsConfigured(), "需要配置我们的助手")
		require.True(t, repoHelper.IsOurs(), "本应是我们的助手，但结果是%q", repoHelper.Cmd)

		gistHelper, err := hc.ConfiguredHelper("gist.github.com")
		require.NoError(t, err)
		require.True(t, gistHelper.IsConfigured(), "需要配置我们的助手")
		require.True(t, gistHelper.IsOurs(), "本应是我们的助手，但结果是%q", gistHelper.Cmd)
	})

	t.Run("当存在全局凭证助手时，应进行配置，但不应使用我们的", func(t *testing.T) {
		hc := contract.NewHelperConfig(t)
		contract.ConfigureHelper(t, "credential.helper")

		helper, err := hc.ConfiguredHelper("github.com")
		require.NoError(t, err)
		require.True(t, helper.IsConfigured(), "期望助手已配置")
		require.False(t, helper.IsOurs(), "预期助手不是我们的，但结果是%q", helper.Cmd)
	})

	t.Run("当存在主机凭证助手时，它应该被配置，但不是我们的", func(t *testing.T) {
		hc := contract.NewHelperConfig(t)
		contract.ConfigureHelper(t, "credential.https://github.com.helper")

		helper, err := hc.ConfiguredHelper("github.com")
		require.NoError(t, err)
		require.True(t, helper.IsConfigured(), "期望助手已配置")
		require.False(t, helper.IsOurs(), "预期助手不是我们的，但结果是%q", helper.Cmd)
	})

	t.Run("当未配置助手时，返回未配置的助手", func(t *testing.T) {
		hc := contract.NewHelperConfig(t)

		helper, err := hc.ConfiguredHelper("github.com")
		require.NoError(t, err)
		require.False(t, helper.IsConfigured(), "未配置任何辅助程序")
		require.False(t, helper.IsOurs(), "预期助手不是我们的，但结果是%q", helper.Cmd)
	})
}
