package view

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/spf13/cobra"
)

type ViewOptions struct {
	IO         *iostreams.IOStreams
	HTTPClient func() (*http.Client, error)
	Config     func() (gh.Config, error)
	Template   string
}

func NewCmdView(f *cmdutil.Factory, runF func(*ViewOptions) error) *cobra.Command {
	opts := &ViewOptions{
		IO:         f.IOStreams,
		HTTPClient: f.HttpClient,
		Config:     f.Config,
		Template:   "",
	}

	cmd := &cobra.Command{
		Use:   "view <template>",
		Short: "查看可用的 gitignore 模板仓库",
		Long: heredoc.Docf(`
			查看可用的仓库 %[1]s.gitignore%[1]s 模板。

			%[1]s<template>%[1]s 是一个区分大小写的 %[1]s.gitignore%[1]s 模板名称。

			要查看可用模板列表，请运行 %[1]sgh repo gitignore list%[1]s。
		`, "`"),
		Example: heredoc.Doc(`
			# 查看 Go gitignore 模板
			$ gh repo gitignore view Go

			# 查看 Python 的 gitignore 模板
			$ gh repo gitignore view Python

			# 使用 Go 模板创建一个新的 .gitignore 文件
			$ gh repo gitignore view Go > .gitignore

			# 使用 Python 模板创建一个新的 .gitignore 文件
			$ gh repo gitignore view Python > .gitignore
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Template = args[0]
			if runF != nil {
				return runF(opts)
			}
			return viewRun(opts)
		},
	}
	return cmd
}

func viewRun(opts *ViewOptions) error {
	if opts.Template == "" {
		return errors.New("未提供模板")
	}

	client, err := opts.HTTPClient()
	if err != nil {
		return err
	}

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	if err := opts.IO.StartPager(); err != nil {
		fmt.Fprintf(opts.IO.ErrOut, "启动分页器失败: %v\n", err)
	}
	defer opts.IO.StopPager()

	hostname, _ := cfg.Authentication().DefaultHost()
	gitIgnore, err := api.RepoGitIgnoreTemplate(client, hostname, opts.Template)
	if err != nil {
		if httpErr, ok := errors.AsType[api.HTTPError](err); ok {
			if httpErr.StatusCode == 404 {
				return fmt.Errorf("'%s' 不是有效的 gitignore 模板名称，请运行 `gh repo gitignore list` 以查看可用列表。", opts.Template)
			}
		}
		return err
	}

	return renderGitIgnore(gitIgnore, opts)
}

func renderGitIgnore(licenseTemplate *api.GitIgnore, opts *ViewOptions) error {
	// I wanted to render this in a markdown code block and benefit
	// from .gitignore syntax highlighting. But, the upstream syntax highlighter
	// does not currently support .gitignore.
	// So, I just add a newline and print the content as is instead.
	// Ref: https://github.com/alecthomas/chroma/pull/755
	var out strings.Builder
	if opts.IO.IsStdoutTTY() {
		out.WriteString("\n")
	}
	out.WriteString(licenseTemplate.Source)
	_, err := opts.IO.Out.Write([]byte(out.String()))
	if err != nil {
		return err
	}
	return nil
}
