package gitcredentials

import (
	"bytes"
	"context"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/git"
)

// Updater 用于更新给定主机名的 git 凭据。
type Updater struct {
	GitClient *git.Client
}

// Update 更新给定主机名的 git 凭据，首先拒绝任何现有凭据，
// 然后批准新凭据。
func (u *Updater) Update(hostname, username, password string) error {
	ctx := context.TODO()

	// 清除之前缓存的凭据
	rejectCmd, err := u.GitClient.Command(ctx, "credential", "reject")
	if err != nil {
		return err
	}

	rejectCmd.Stdin = bytes.NewBufferString(heredoc.Docf(`
		protocol=https
		host=%s
	`, hostname))

	_, err = rejectCmd.Output()
	if err != nil {
		return err
	}

	approveCmd, err := u.GitClient.Command(ctx, "credential", "approve")
	if err != nil {
		return err
	}

	approveCmd.Stdin = bytes.NewBufferString(heredoc.Docf(`
		protocol=https
		host=%s
		username=%s
		password=%s
	`, hostname, username, password))

	_, err = approveCmd.Output()
	if err != nil {
		return err
	}

	return nil
}
