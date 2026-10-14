package accessibility

import (
	"fmt"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/text"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

const (
	acrURL             = "https://accessibility.github.com/conformance/cli/"
	a11yDiscussionsURL = "https://github.com/orgs/community/discussions/categories/accessibility"
)

type AccessibilityOptions struct {
	IO      *iostreams.IOStreams
	Browser browser.Browser
	Web     bool
}

func NewCmdAccessibility(f *cmdutil.Factory) *cobra.Command {
	opts := AccessibilityOptions{
		IO:      f.IOStreams,
		Browser: f.Browser,
	}

	cmd := &cobra.Command{
		Use:     "accessibility",
		Aliases: []string{"a11y"},
		Short:   "了解 GitHub CLI 的无障碍体验功能",
		Long:    longDescription(opts.IO),
		Hidden:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Web {
				if opts.IO.IsStdoutTTY() {
					fmt.Fprintf(opts.IO.ErrOut, "正在浏览器中打开 %s\n", text.DisplayURL(acrURL))
				}
				return opts.Browser.Browse(acrURL)
			}

			return cmd.Help()
		},
		Example: heredoc.Doc(`
			# 在浏览器中打开 GitHub 可访问性网站
			$ gh accessibility --web

			# 使用可自定义的、4位可访问的颜色显示颜色
			$ gh config set accessible_colors enabled

			# 使用输入提示而不重绘屏幕
			$ gh config set accessible_prompter enabled

			# 禁用进度指示器中的基于运动的旋转动画，改用文本显示
			$ gh config set spinner disabled
		`),
	}

	cmd.Flags().BoolVarP(&opts.Web, "web", "w", false, "在浏览器中打开 GitHub 可访问性网站")
	cmdutil.DisableAuthCheck(cmd)

	return cmd
}

func longDescription(io *iostreams.IOStreams) string {
	cs := io.ColorScheme()
	title := cs.Bold("了解 GitHub CLI 的无障碍体验功能")
	color := cs.Bold("可定制且对比鲜明的颜色")
	prompter := cs.Bold("非交互式用户输入提示")
	spinner := cs.Bold("基于文本的旋转器")
	feedback := cs.Bold("加入对话")

	return heredoc.Docf(`
		%[2]s

		作为所有开发者的家园，我们希望每位开发者都能在我们的社区中感到受欢迎，并能够利用 GitHub 提供的
		一切(包括 GitHub CLI)为全球软件开发的未来贡献力量。

		%[3]s

		文本界面通常会出于各种目的使用颜色，但对比度不足或可定制性差可能会使某些用户无法从中受益。

		为了提供更便捷的体验，GitHub CLI 可以根据终端背景外观使用调色板，并将颜色限制为 4 位 ANSI 调色板，
		用户可以在终端首选项中自定义这些调色板。

		通过这一新体验，GitHub CLI 提供了多种选项来处理颜色使用问题:

		1. GitHub CLI 将使用 4 位调色板来增强颜色对比度，该调色板适用于深色和浅色背景，并根据 GitHub Primer
		设计系统渲染 Markdown。

		   要启用此体验，请使用以下方法之一:
		   - 运行 %[1]sgh config set accessible_colors enabled%[1]s
		   - 配置 %[1]sGH_ACCESSIBLE_COLORS=enabled%[1]s 环境变量

		2. GitHub CLI 将在支持真彩色的终端中显示问题和拉取请求标签的自定义 RGB 颜色。

		   要启用此体验，请使用以下方法之一:
		   - 运行 %[1]sgh config set color_labels enabled%[1]s
		   - 配置 %[1]sGH_COLOR_LABELS=enabled%[1]s 环境变量

		%[4]s

		交互式文本用户界面通过操纵终端光标来重绘屏幕的部分内容，这对于语音合成器或盲文显示器来说可能难以准确检测和读取。

		为了提供更便捷的体验，GitHub CLI 可以通过非交互式提示来获取用户输入，从而实现类似的体验。

		要启用此体验，请使用以下方法之一:
		- 运行 %[1]sgh config set accessible_prompter enabled%[1]s
		- 配置 %[1]sGH_ACCESSIBLE_PROMPTER=enabled%[1]s 环境变量

		%[5]s

		基于运动的加载动画通过操纵终端光标来产生旋转效果，以传达正在进行的活动状态，但这可能会给有运动敏感的用户带来不适，
		或向语音合成器传递错误的信息。

		为了获得更便捷的体验，可以禁用此交互功能，转而使用基于文本的进度指示器。

		要启用此体验，请使用以下方法之一:
		- 运行 %[1]sgh config set spinner disabled%[1]s
		- 配置 %[1]sGH_SPINNER_DISABLED=yes%[1]s 环境变量

		%[6]s

		我们邀请您通过 GitHub 可访问性反馈渠道分享您的意见和想法，与我们共同提升 GitHub CLI 的可访问性:

		%[7]s
	`, "`", title, color, prompter, spinner, feedback, a11yDiscussionsURL)
}
