package codespace

import (
	"context"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func newCodeCmd(app *App) *cobra.Command {
	var (
		selector    *CodespaceSelector
		useInsiders bool
		useWeb      bool
	)

	codeCmd := &cobra.Command{
		Use:   "code",
		Short: "在 Visual Studio Code 中打开 codespace",
		Args:  noArgsConstraint,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.VSCode(cmd.Context(), selector, useInsiders, useWeb)
		},
	}

	selector = AddCodespaceSelector(codeCmd, app.apiClient)

	codeCmd.Flags().BoolVar(&useInsiders, "insiders", false, "使用 Visual Studio Code 的 insiders 版本")
	codeCmd.Flags().BoolVarP(&useWeb, "web", "w", false, "使用 Visual Studio Code 的网页版本")

	return codeCmd
}

// VSCode 在本地 VSCode 应用程序中打开 codespace。
func (a *App) VSCode(ctx context.Context, selector *CodespaceSelector, useInsiders bool, useWeb bool) error {
	codespace, err := selector.Select(ctx)
	if err != nil {
		return err
	}

	browseURL := vscodeProtocolURL(codespace.Name, useInsiders)
	if useWeb {
		browseURL = codespace.WebURL
		if useInsiders {
			u, err := url.Parse(browseURL)
			if err != nil {
				return err
			}
			q := u.Query()
			q.Set("vscodeChannel", "insiders")
			u.RawQuery = q.Encode()
			browseURL = u.String()
		}
	}

	if err := a.browser.Browse(browseURL); err != nil {
		return fmt.Errorf("打开 Visual Studio Code 时出错: %w", err)
	}

	return nil
}

func vscodeProtocolURL(codespaceName string, useInsiders bool) string {
	application := "vscode"
	if useInsiders {
		application = "vscode-insiders"
	}
	return fmt.Sprintf("%s://github.codespaces/connect?name=%s&windowId=_blank", application, url.QueryEscape(codespaceName))
}
