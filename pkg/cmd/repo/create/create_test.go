package create

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	"github.com/cenkalti/backoff/v4"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/internal/gh"
	ghmock "github.com/cli/cli/v2/internal/gh/mock"
	"github.com/cli/cli/v2/internal/prompter"
	"github.com/cli/cli/v2/internal/run"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/google/shlex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCmdCreate(t *testing.T) {
	tests := []struct {
		name      string
		tty       bool
		cli       string
		wantsErr  bool
		errMsg    string
		wantsOpts CreateOptions
	}{
		{
			name:      "no args tty",
			tty:       true,
			cli:       "",
			wantsOpts: CreateOptions{Interactive: true},
		},
		{
			name:     "no args no-tty",
			tty:      false,
			cli:      "",
			wantsErr: true,
			errMsg:   "非交互模式下至少需要一个参数",
		},
		{
			name: "new repo from remote",
			cli:  "NEWREPO --public --clone",
			wantsOpts: CreateOptions{
				Name:   "NEWREPO",
				Public: true,
				Clone:  true},
		},
		{
			name:     "no visibility",
			tty:      true,
			cli:      "NEWREPO",
			wantsErr: true,
			errMsg:   "非交互模式下运行时，需要 `--public`, `--private` 或 `--internal` 之一",
		},
		{
			name:     "multiple visibility",
			tty:      true,
			cli:      "NEWREPO --public --private",
			wantsErr: true,
			errMsg:   "`--public`, `--private` 或 `--internal` 中只能提供一个",
		},
		{
			name: "new remote from local",
			cli:  "--source=/path/to/repo --private",
			wantsOpts: CreateOptions{
				Private: true,
				Source:  "/path/to/repo"},
		},
		{
			name: "new remote from local with remote",
			cli:  "--source=/path/to/repo --public --remote upstream",
			wantsOpts: CreateOptions{
				Public: true,
				Source: "/path/to/repo",
				Remote: "upstream",
			},
		},
		{
			name: "new remote from local with push",
			cli:  "--source=/path/to/repo --push --public",
			wantsOpts: CreateOptions{
				Public: true,
				Source: "/path/to/repo",
				Push:   true,
			},
		},
		{
			name: "new remote from local without visibility",
			cli:  "--source=/path/to/repo --push",
			wantsOpts: CreateOptions{
				Source: "/path/to/repo",
				Push:   true,
			},
			wantsErr: true,
			errMsg:   "非交互模式下运行时，需要 `--public`, `--private` 或 `--internal` 之一",
		},
		{
			name:     "source with template",
			cli:      "--source=/path/to/repo --private --template mytemplate",
			wantsErr: true,
			errMsg:   "`--source` 选项不支持与 `--clone`、`--template`, `--license` 或 `--gitignore` 搭配使用",
		},
		{
			name:     "include all branches without template",
			cli:      "--source=/path/to/repo --private --include-all-branches",
			wantsErr: true,
			errMsg:   "`--include-all-branches` 选项仅在使用 `--template` 时受支持",
		},
		{
			name: "new remote from template with include all branches",
			cli:  "template-repo --template https://github.com/OWNER/REPO --public --include-all-branches",
			wantsOpts: CreateOptions{
				Name:               "template-repo",
				Public:             true,
				Template:           "https://github.com/OWNER/REPO",
				IncludeAllBranches: true,
			},
		},
		{
			name:     "template with .gitignore",
			cli:      "template-repo --template mytemplate --gitignore ../.gitignore --public",
			wantsErr: true,
			errMsg:   "提供了模板时，不会添加 .gitignore 和许可证模板",
		},
		{
			name:     "template with license",
			cli:      "template-repo --template mytemplate --license ../.license --public",
			wantsErr: true,
			errMsg:   "提供了模板时，不会添加 .gitignore 和许可证模板",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			ios.SetStdinTTY(tt.tty)
			ios.SetStdoutTTY(tt.tty)

			f := &cmdutil.Factory{
				IOStreams: ios,
			}

			var opts *CreateOptions
			cmd := NewCmdCreate(f, func(o *CreateOptions) error {
				opts = o
				return nil
			})

			// TODO STUPID HACK
			// cobra aggressively adds help to all commands. since we're not running through the root command
			// (which manages help when running for real) and since create has a '-h' flag (for homepage),
			// cobra blows up when it tried to add a help flag and -h is already in use. This hack adds a
			// dummy help flag with a random shorthand to get around this.
			cmd.Flags().BoolP("help", "x", false, "")

			args, err := shlex.Split(tt.cli)
			require.NoError(t, err)
			cmd.SetArgs(args)
			cmd.SetIn(&bytes.Buffer{})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			_, err = cmd.ExecuteC()
			if tt.wantsErr {
				assert.Error(t, err)
				assert.Equal(t, tt.errMsg, err.Error())
				return
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantsOpts.Interactive, opts.Interactive)
			assert.Equal(t, tt.wantsOpts.Source, opts.Source)
			assert.Equal(t, tt.wantsOpts.Name, opts.Name)
			assert.Equal(t, tt.wantsOpts.Public, opts.Public)
			assert.Equal(t, tt.wantsOpts.Internal, opts.Internal)
			assert.Equal(t, tt.wantsOpts.Private, opts.Private)
			assert.Equal(t, tt.wantsOpts.Clone, opts.Clone)
		})
	}
}

func Test_createRun(t *testing.T) {
	tests := []struct {
		name        string
		tty         bool
		opts        *CreateOptions
		httpStubs   func(*httpmock.Registry)
		promptStubs func(*prompter.PrompterMock)
		execStubs   func(*run.CommandStubber)
		wantStdout  string
		wantErr     bool
		errMsg      string
	}{
		{
			name: "interactive create from scratch with empty name",
			opts: &CreateOptions{Interactive: true},
			tty:  true,
			promptStubs: func(p *prompter.PrompterMock) {
				p.InputFunc = func(message, defaultValue string) (string, error) {
					if message != "仓库名称" || len(p.InputCalls()) != 1 {
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
					return "", nil
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "您想做什么?":
						return prompter.IndexFor(options, "在 github.com 上从零开始创建新仓库")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			wantErr: true,
			errMsg:  "仓库名称不能为空",
		},
		{
			name: "interactive create from scratch with whitespace-only name",
			opts: &CreateOptions{Interactive: true},
			tty:  true,
			promptStubs: func(p *prompter.PrompterMock) {
				p.InputFunc = func(message, defaultValue string) (string, error) {
					if message != "仓库名称" || len(p.InputCalls()) != 1 {
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
					return " \t ", nil
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "您想做什么?":
						return prompter.IndexFor(options, "在 github.com 上从零开始创建新仓库")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			wantErr: true,
			errMsg:  "仓库名称不能为空",
		},
		{
			name:       "interactive create from scratch with gitignore and license",
			opts:       &CreateOptions{Interactive: true},
			tty:        true,
			wantStdout: "✓ 已在 github.com 上创建仓库 OWNER/REPO\n  https://github.com/OWNER/REPO\n",
			promptStubs: func(p *prompter.PrompterMock) {
				p.ConfirmFunc = func(message string, defaultValue bool) (bool, error) {
					switch message {
					case "您想添加 README 文件吗?":
						return false, nil
					case "您想添加 .gitignore 吗?":
						return true, nil
					case "您想添加许可证吗?":
						return true, nil
					case `将在 github.com 上以 private 可见性创建 "REPO"，是否继续?`:
						return defaultValue, nil
					case "是否在本地克隆新仓库?":
						return defaultValue, nil
					default:
						return false, fmt.Errorf("unexpected confirm prompt: %s", message)
					}
				}
				p.InputFunc = func(message, defaultValue string) (string, error) {
					switch message {
					case "仓库名称":
						return "REPO", nil
					case "描述":
						return "my new repo", nil
					default:
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "您想做什么?":
						return prompter.IndexFor(options, "在 github.com 上从零开始创建新仓库")
					case "可见性":
						return prompter.IndexFor(options, "Private")
					case "选择一个许可证":
						return prompter.IndexFor(options, "GNU Lesser General Public License v3.0")
					case "选择一个 .gitignore 模板":
						return prompter.IndexFor(options, "Go")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"someuser","organizations":{"nodes": []}}}}`))
				reg.Register(
					httpmock.REST("GET", "gitignore/templates"),
					httpmock.StringResponse(`["Actionscript","Android","AppceleratorTitanium","Autotools","Bancha","C","C++","Go"]`))
				reg.Register(
					httpmock.REST("GET", "licenses"),
					httpmock.StringResponse(`[{"key": "mit","name": "MIT License"},{"key": "lgpl-3.0","name": "GNU Lesser General Public License v3.0"}]`))
				reg.Register(
					httpmock.REST("POST", "user/repos"),
					httpmock.StringResponse(`{"name":"REPO", "owner":{"login": "OWNER"}, "html_url":"https://github.com/OWNER/REPO"}`))

			},
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git clone https://github.com/OWNER/REPO.git`, 0, "")
			},
		},
		{
			name:       "interactive create from scratch but with prompted owner",
			opts:       &CreateOptions{Interactive: true},
			tty:        true,
			wantStdout: "✓ 已在 github.com 上创建仓库 org1/REPO\n  https://github.com/org1/REPO\n",
			promptStubs: func(p *prompter.PrompterMock) {
				p.ConfirmFunc = func(message string, defaultValue bool) (bool, error) {
					switch message {
					case "您想添加 README 文件吗?":
						return false, nil
					case "您想添加 .gitignore 吗?":
						return false, nil
					case "您想添加许可证吗?":
						return false, nil
					case `将在 github.com 上以 private 可见性创建 "org1/REPO"，是否继续?`:
						return true, nil
					case "是否在本地克隆新仓库?":
						return false, nil
					default:
						return false, fmt.Errorf("unexpected confirm prompt: %s", message)
					}
				}
				p.InputFunc = func(message, defaultValue string) (string, error) {
					switch message {
					case "仓库名称":
						return "REPO", nil
					case "描述":
						return "my new repo", nil
					default:
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "仓库所有者":
						return prompter.IndexFor(options, "org1")
					case "您想做什么?":
						return prompter.IndexFor(options, "在 github.com 上从零开始创建新仓库")
					case "可见性":
						return prompter.IndexFor(options, "Private")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"someuser","organizations":{"nodes": [{"login": "org1"}, {"login": "org2"}]}}}}`))
				reg.Register(
					httpmock.REST("GET", "users/org1"),
					httpmock.StringResponse(`{"login":"org1","type":"Organization"}`))
				reg.Register(
					httpmock.GraphQL(`mutation RepositoryCreate\b`),
					httpmock.StringResponse(`
					{
						"data": {
							"createRepository": {
								"repository": {
									"id": "REPOID",
									"name": "REPO",
									"owner": {"login":"org1"},
									"url": "https://github.com/org1/REPO"
								}
							}
						}
					}`))
			},
		},
		{
			name: "interactive create from scratch but cancel before submit",
			opts: &CreateOptions{Interactive: true},
			tty:  true,
			promptStubs: func(p *prompter.PrompterMock) {
				p.ConfirmFunc = func(message string, defaultValue bool) (bool, error) {
					switch message {
					case "您想添加 README 文件吗?":
						return false, nil
					case "您想添加 .gitignore 吗?":
						return false, nil
					case "您想添加许可证吗?":
						return false, nil
					case `将在 github.com 上以 private 可见性创建 "REPO"，是否继续?`:
						return false, nil
					default:
						return false, fmt.Errorf("unexpected confirm prompt: %s", message)
					}
				}
				p.InputFunc = func(message, defaultValue string) (string, error) {
					switch message {
					case "仓库名称":
						return "REPO", nil
					case "描述":
						return "my new repo", nil
					default:
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "您想做什么?":
						return prompter.IndexFor(options, "在 github.com 上从零开始创建新仓库")
					case "可见性":
						return prompter.IndexFor(options, "Private")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"someuser","organizations":{"nodes": []}}}}`))
			},
			wantStdout: "",
			wantErr:    true,
			errMsg:     "CancelError",
		},
		{
			name: "interactive with existing repository public",
			opts: &CreateOptions{Interactive: true},
			tty:  true,
			promptStubs: func(p *prompter.PrompterMock) {
				p.ConfirmFunc = func(message string, defaultValue bool) (bool, error) {
					switch message {
					case "是否添加远程?":
						return false, nil
					default:
						return false, fmt.Errorf("unexpected confirm prompt: %s", message)
					}
				}
				p.InputFunc = func(message, defaultValue string) (string, error) {
					switch message {
					case "本地仓库路径":
						return defaultValue, nil
					case "仓库名称":
						return "REPO", nil
					case "描述":
						return "my new repo", nil
					default:
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "您想做什么?":
						return prompter.IndexFor(options, "将现有的本地仓库推送到 github.com")
					case "可见性":
						return prompter.IndexFor(options, "Private")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"someuser","organizations":{"nodes": []}}}}`))
				reg.Register(
					httpmock.GraphQL(`mutation RepositoryCreate\b`),
					httpmock.StringResponse(`
					{
						"data": {
							"createRepository": {
								"repository": {
									"id": "REPOID",
									"name": "REPO",
									"owner": {"login":"OWNER"},
									"url": "https://github.com/OWNER/REPO"
								}
							}
						}
					}`))
			},
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git -C . rev-parse --git-dir`, 0, ".git")
				cs.Register(`git -C . rev-parse HEAD`, 0, "commithash")
			},
			wantStdout: "✓ 已在 github.com 上创建仓库 OWNER/REPO\n  https://github.com/OWNER/REPO\n",
		},
		{
			name: "interactive with existing bare repository public and push",
			opts: &CreateOptions{Interactive: true},
			tty:  true,
			promptStubs: func(p *prompter.PrompterMock) {
				p.ConfirmFunc = func(message string, defaultValue bool) (bool, error) {
					switch message {
					case "是否添加远程?":
						return true, nil
					case `是否将所有引用镜像到 "origin"?`:
						return true, nil
					default:
						return false, fmt.Errorf("unexpected confirm prompt: %s", message)
					}
				}
				p.InputFunc = func(message, defaultValue string) (string, error) {
					switch message {
					case "本地仓库路径":
						return defaultValue, nil
					case "仓库名称":
						return "REPO", nil
					case "描述":
						return "my new repo", nil
					case "新的远程应叫什么名字?":
						return defaultValue, nil
					default:
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "您想做什么?":
						return prompter.IndexFor(options, "将现有的本地仓库推送到 github.com")
					case "可见性":
						return prompter.IndexFor(options, "Private")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"someuser","organizations":{"nodes": []}}}}`))
				reg.Register(
					httpmock.GraphQL(`mutation RepositoryCreate\b`),
					httpmock.StringResponse(`
					{
						"data": {
							"createRepository": {
								"repository": {
									"id": "REPOID",
									"name": "REPO",
									"owner": {"login":"OWNER"},
									"url": "https://github.com/OWNER/REPO"
								}
							}
						}
					}`))
			},
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git -C . rev-parse --git-dir`, 0, ".")
				cs.Register(`git -C . rev-parse HEAD`, 0, "commithash")
				cs.Register(`git -C . remote add origin https://github.com/OWNER/REPO`, 0, "")
				cs.Register(`git -C . push origin --mirror`, 0, "")
			},
			wantStdout: "✓ 已在 github.com 上创建仓库 OWNER/REPO\n  https://github.com/OWNER/REPO\n✓ 已添加远程 https://github.com/OWNER/REPO.git\n✓ 已将所有引用镜像到 https://github.com/OWNER/REPO.git\n",
		},
		{
			name: "interactive with existing repository public add remote and push",
			opts: &CreateOptions{Interactive: true},
			tty:  true,
			promptStubs: func(p *prompter.PrompterMock) {
				p.ConfirmFunc = func(message string, defaultValue bool) (bool, error) {
					switch message {
					case "是否添加远程?":
						return true, nil
					case `是否将当前分支的提交推送到 "origin"?`:
						return true, nil
					default:
						return false, fmt.Errorf("unexpected confirm prompt: %s", message)
					}
				}
				p.InputFunc = func(message, defaultValue string) (string, error) {
					switch message {
					case "本地仓库路径":
						return defaultValue, nil
					case "仓库名称":
						return "REPO", nil
					case "描述":
						return "my new repo", nil
					case "新的远程应叫什么名字?":
						return defaultValue, nil
					default:
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "您想做什么?":
						return prompter.IndexFor(options, "将现有的本地仓库推送到 github.com")
					case "可见性":
						return prompter.IndexFor(options, "Private")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"someuser","organizations":{"nodes": []}}}}`))
				reg.Register(
					httpmock.GraphQL(`mutation RepositoryCreate\b`),
					httpmock.StringResponse(`
					{
						"data": {
							"createRepository": {
								"repository": {
									"id": "REPOID",
									"name": "REPO",
									"owner": {"login":"OWNER"},
									"url": "https://github.com/OWNER/REPO"
								}
							}
						}
					}`))
			},
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git -C . rev-parse --git-dir`, 0, ".git")
				cs.Register(`git -C . rev-parse HEAD`, 0, "commithash")
				cs.Register(`git -C . remote add origin https://github.com/OWNER/REPO`, 0, "")
				cs.Register(`git -C . push --set-upstream origin HEAD`, 0, "")
			},
			wantStdout: "✓ 已在 github.com 上创建仓库 OWNER/REPO\n  https://github.com/OWNER/REPO\n✓ 已添加远程 https://github.com/OWNER/REPO.git\n✓ 已将提交推送到 https://github.com/OWNER/REPO.git\n",
		},
		{
			name: "interactive create from a template repository",
			opts: &CreateOptions{Interactive: true},
			tty:  true,
			promptStubs: func(p *prompter.PrompterMock) {
				p.ConfirmFunc = func(message string, defaultValue bool) (bool, error) {
					switch message {
					case `将在 github.com 上以 private 可见性创建 "OWNER/REPO"，是否继续?`:
						return defaultValue, nil
					case "是否在本地克隆新仓库?":
						return defaultValue, nil
					default:
						return false, fmt.Errorf("unexpected confirm prompt: %s", message)
					}
				}
				p.InputFunc = func(message, defaultValue string) (string, error) {
					switch message {
					case "仓库名称":
						return "REPO", nil
					case "描述":
						return "my new repo", nil
					default:
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "仓库所有者":
						return prompter.IndexFor(options, "OWNER")
					case "选择一个模板仓库":
						return prompter.IndexFor(options, "REPO")
					case "您想做什么?":
						return prompter.IndexFor(options, "在 github.com 上从模板仓库创建新仓库")
					case "可见性":
						return prompter.IndexFor(options, "Private")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"OWNER","organizations":{"nodes": []}}}}`))
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"OWNER","organizations":{"nodes": []}}}}`))
				reg.Register(
					httpmock.GraphQL(`query RepositoryList\b`),
					httpmock.FileResponse("./fixtures/repoTempList.json"))
				reg.Register(
					httpmock.REST("GET", "users/OWNER"),
					httpmock.StringResponse(`{"login":"OWNER","type":"User"}`))
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"id":"OWNER"}}}`))
				reg.Register(
					httpmock.GraphQL(`mutation CloneTemplateRepository\b`),
					httpmock.StringResponse(`
						{
							"data": {
								"cloneTemplateRepository": {
									"repository": {
										"id": "REPOID",
										"name": "REPO",
										"owner": {"login":"OWNER"},
										"url": "https://github.com/OWNER/REPO"
									}
								}
							}
						}`))
			},
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git clone --branch main https://github.com/OWNER/REPO`, 0, "")
			},
			wantStdout: "✓ 已在 github.com 上创建仓库 OWNER/REPO\n  https://github.com/OWNER/REPO\n",
		},
		{
			name: "interactive create from template repo but there are no template repos",
			opts: &CreateOptions{Interactive: true},
			tty:  true,
			promptStubs: func(p *prompter.PrompterMock) {
				p.ConfirmFunc = func(message string, defaultValue bool) (bool, error) {
					switch message {
					default:
						return false, fmt.Errorf("unexpected confirm prompt: %s", message)
					}
				}
				p.InputFunc = func(message, defaultValue string) (string, error) {
					switch message {
					case "仓库名称":
						return "REPO", nil
					case "描述":
						return "my new repo", nil
					default:
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "您想做什么?":
						return prompter.IndexFor(options, "在 github.com 上从模板仓库创建新仓库")
					case "可见性":
						return prompter.IndexFor(options, "Private")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"OWNER","organizations":{"nodes": []}}}}`))
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"OWNER","organizations":{"nodes": []}}}}`))
				reg.Register(
					httpmock.GraphQL(`query RepositoryList\b`),
					httpmock.StringResponse(`{"data":{"repositoryOwner":{"login":"OWNER","repositories":{"nodes":[]},"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`))
			},
			execStubs:  func(cs *run.CommandStubber) {},
			wantStdout: "",
			wantErr:    true,
			errMsg:     "OWNER 没有模板仓库",
		},
		{
			name: "noninteractive create from scratch",
			opts: &CreateOptions{
				Interactive: false,
				Name:        "REPO",
				Visibility:  "PRIVATE",
			},
			tty: false,
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`mutation RepositoryCreate\b`),
					httpmock.StringResponse(`
					{
						"data": {
							"createRepository": {
								"repository": {
									"id": "REPOID",
									"name": "REPO",
									"owner": {"login":"OWNER"},
									"url": "https://github.com/OWNER/REPO"
								}
							}
						}
					}`))
			},
			wantStdout: "https://github.com/OWNER/REPO\n",
		},
		{
			name: "noninteractive create from source",
			opts: &CreateOptions{
				Interactive: false,
				Source:      ".",
				Name:        "REPO",
				Visibility:  "PRIVATE",
			},
			tty: false,
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`mutation RepositoryCreate\b`),
					httpmock.StringResponse(`
					{
						"data": {
							"createRepository": {
								"repository": {
									"id": "REPOID",
									"name": "REPO",
									"owner": {"login":"OWNER"},
									"url": "https://github.com/OWNER/REPO"
								}
							}
						}
					}`))
			},
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git -C . rev-parse --git-dir`, 0, ".git")
				cs.Register(`git -C . rev-parse HEAD`, 0, "commithash")
				cs.Register(`git -C . remote add origin https://github.com/OWNER/REPO`, 0, "")
			},
			wantStdout: "https://github.com/OWNER/REPO\n",
		},
		{
			name: "noninteractive create bare from source and push",
			opts: &CreateOptions{
				Interactive: false,
				Source:      ".",
				Push:        true,
				Name:        "REPO",
				Visibility:  "PRIVATE",
			},
			tty: false,
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`mutation RepositoryCreate\b`),
					httpmock.StringResponse(`
						{
							"data": {
								"createRepository": {
									"repository": {
										"id": "REPOID",
										"name": "REPO",
										"owner": {"login":"OWNER"},
										"url": "https://github.com/OWNER/REPO"
									}
								}
							}
						}`))
			},
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git -C . rev-parse --git-dir`, 0, ".")
				cs.Register(`git -C . rev-parse HEAD`, 0, "commithash")
				cs.Register(`git -C . remote add origin https://github.com/OWNER/REPO`, 0, "")
				cs.Register(`git -C . push origin --mirror`, 0, "")
			},
			wantStdout: "https://github.com/OWNER/REPO\n",
		},
		{
			name: "noninteractive create from cwd that isn't a git repo",
			opts: &CreateOptions{
				Interactive: false,
				Source:      ".",
				Name:        "REPO",
				Visibility:  "PRIVATE",
			},
			tty: false,
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git -C . rev-parse --git-dir`, 128, "")
			},
			wantErr: true,
			errMsg:  "当前目录不是 git 仓库，请运行 `git init` 进行初始化",
		},
		{
			name: "noninteractive create from cwd that isn't a git repo",
			opts: &CreateOptions{
				Interactive: false,
				Source:      "some-dir",
				Name:        "REPO",
				Visibility:  "PRIVATE",
			},
			tty: false,
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git -C some-dir rev-parse --git-dir`, 128, "")
			},
			wantErr: true,
			errMsg:  "some-dir 不是 git 仓库，请运行 `git -C \"some-dir\" init` 进行初始化",
		},
		{
			name: "noninteractive clone from scratch",
			opts: &CreateOptions{
				Interactive: false,
				Name:        "REPO",
				Visibility:  "PRIVATE",
				Clone:       true,
			},
			tty: false,
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`mutation RepositoryCreate\b`),
					httpmock.StringResponse(`
					{
						"data": {
							"createRepository": {
								"repository": {
									"id": "REPOID",
									"name": "REPO",
									"owner": {"login":"OWNER"},
									"url": "https://github.com/OWNER/REPO"
								}
							}
						}
					}`))
			},
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git init REPO`, 0, "")
				cs.Register(`git -C REPO remote add origin https://github.com/OWNER/REPO`, 0, "")
			},
			wantStdout: "https://github.com/OWNER/REPO\n",
		},
		{
			name: "noninteractive clone with readme",
			opts: &CreateOptions{
				Interactive: false,
				Name:        "ElliotAlderson",
				Visibility:  "PRIVATE",
				Clone:       true,
				AddReadme:   true,
			},
			tty: false,
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.REST("POST", "user/repos"),
					httpmock.RESTPayload(200, "{\"name\":\"ElliotAlderson\", \"owner\":{\"login\": \"OWNER\"}, \"html_url\":\"https://github.com/OWNER/ElliotAlderson\"}",
						func(payload map[string]any) {
							payload["name"] = "ElliotAlderson"
							payload["owner"] = map[string]any{"login": "OWNER"}
							payload["auto_init"] = true
							payload["private"] = true
						},
					),
				)
			},
			execStubs: func(cs *run.CommandStubber) {
				cs.Register(`git clone https://github.com/OWNER/ElliotAlderson`, 128, "")
				cs.Register(`git clone https://github.com/OWNER/ElliotAlderson`, 0, "")
			},
			wantStdout: "https://github.com/OWNER/ElliotAlderson\n",
		},
		{
			name: "noninteractive create from template with retry",
			opts: &CreateOptions{
				Interactive: false,
				Name:        "REPO",
				Visibility:  "PRIVATE",
				Clone:       true,
				Template:    "mytemplate",
				BackOff:     &backoff.ZeroBackOff{},
			},
			tty: false,
			httpStubs: func(reg *httpmock.Registry) {
				// Test resolving repo owner from repo name only.
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"OWNER"}}}`))
				reg.Register(
					httpmock.GraphQL(`query RepositoryInfo\b`),
					httpmock.GraphQLQuery(`{
						"data": {
							"repository": {
								"id": "REPOID",
								"defaultBranchRef": {
									"name": "main"
								}
							}
						}
					}`, func(s string, m map[string]any) {
						assert.Equal(t, "OWNER", m["owner"])
						assert.Equal(t, "mytemplate", m["name"])
					}),
				)
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"id":"OWNERID"}}}`))
				reg.Register(
					httpmock.GraphQL(`mutation CloneTemplateRepository\b`),
					httpmock.GraphQLMutation(`
					{
						"data": {
							"cloneTemplateRepository": {
								"repository": {
									"id": "REPOID",
									"name": "REPO",
									"owner": {"login":"OWNER"},
									"url": "https://github.com/OWNER/REPO"
								}
							}
						}
					}`, func(m map[string]any) {
						assert.Equal(t, "REPOID", m["repositoryId"])
					}))
			},
			execStubs: func(cs *run.CommandStubber) {
				// fatal: Remote branch main not found in upstream origin
				cs.Register(`git clone --branch main https://github.com/OWNER/REPO`, 128, "")
				cs.Register(`git clone --branch main https://github.com/OWNER/REPO`, 0, "")
			},
			wantStdout: "https://github.com/OWNER/REPO\n",
		},
		{
			name: "interactive create from scratch with host override",
			opts: &CreateOptions{
				Interactive: true,
				Config: func() (gh.Config, error) {
					cfg := &ghmock.ConfigMock{
						AuthenticationFunc: func() gh.AuthConfig {
							authCfg := &config.AuthConfig{}
							authCfg.SetHosts([]string{"example.com"})
							authCfg.SetDefaultHost("example.com", "GH_HOST")
							return authCfg
						},
					}
					return cfg, nil
				},
			},
			tty: true,
			promptStubs: func(p *prompter.PrompterMock) {
				p.ConfirmFunc = func(message string, defaultValue bool) (bool, error) {
					switch message {
					case "您想添加 README 文件吗?":
						return false, nil
					case "您想添加 .gitignore 吗?":
						return false, nil
					case "您想添加许可证吗?":
						return false, nil
					case `将在 example.com 上以 private 可见性创建 "REPO"，是否继续?`:
						return defaultValue, nil
					case "是否在本地克隆新仓库?":
						return false, nil
					default:
						return false, fmt.Errorf("unexpected confirm prompt: %s", message)
					}
				}
				p.InputFunc = func(message, defaultValue string) (string, error) {
					switch message {
					case "仓库名称":
						return "REPO", nil
					case "描述":
						return "my new repo", nil
					default:
						return "", fmt.Errorf("unexpected input prompt: %s", message)
					}
				}
				p.SelectFunc = func(message, defaultValue string, options []string) (int, error) {
					switch message {
					case "您想做什么?":
						return prompter.IndexFor(options, "在 example.com 上从零开始创建新仓库")
					case "可见性":
						return prompter.IndexFor(options, "Private")
					case "选择一个许可证":
						return prompter.IndexFor(options, "GNU Lesser General Public License v3.0")
					case "选择一个 .gitignore 模板":
						return prompter.IndexFor(options, "Go")
					default:
						return 0, fmt.Errorf("unexpected select prompt: %s", message)
					}
				}
			},
			httpStubs: func(reg *httpmock.Registry) {
				reg.Register(
					httpmock.GraphQL(`query UserCurrent\b`),
					httpmock.StringResponse(`{"data":{"viewer":{"login":"someuser","organizations":{"nodes": []}}}}`))
				reg.Register(
					httpmock.GraphQL(`mutation RepositoryCreate\b`),
					httpmock.StringResponse(`
						{
							"data": {
								"createRepository": {
									"repository": {
										"id": "REPOID",
										"name": "REPO",
										"owner": {"login":"OWNER"},
										"url": "https://example.com/OWNER/REPO"
									}
								}
							}
						}`),
				)
			},
			wantStdout: "✓ 已在 example.com 上创建仓库 OWNER/REPO\n  https://example.com/OWNER/REPO\n",
		},
	}
	for _, tt := range tests {
		prompterMock := &prompter.PrompterMock{}
		tt.opts.Prompter = prompterMock
		if tt.promptStubs != nil {
			tt.promptStubs(prompterMock)
		}

		reg := &httpmock.Registry{}
		if tt.httpStubs != nil {
			tt.httpStubs(reg)
		}
		tt.opts.HttpClient = func() (*http.Client, error) {
			return &http.Client{Transport: reg}, nil
		}

		if tt.opts.Config == nil {
			tt.opts.Config = func() (gh.Config, error) {
				return config.NewMockConfig(), nil
			}
		}

		tt.opts.GitClient = &git.Client{
			GhPath:  "some/path/gh",
			GitPath: "some/path/git",
		}

		ios, _, stdout, stderr := iostreams.Test()
		ios.SetStdinTTY(tt.tty)
		ios.SetStdoutTTY(tt.tty)
		tt.opts.IO = ios

		t.Run(tt.name, func(t *testing.T) {
			cs, restoreRun := run.Stub()
			defer restoreRun(t)
			if tt.execStubs != nil {
				tt.execStubs(cs)
			}

			defer reg.Verify(t)
			err := createRun(tt.opts)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantStdout, stdout.String())
			assert.Equal(t, "", stderr.String())
		})
	}
}

func Test_getRepoVisibilityOptions(t *testing.T) {
	tests := []struct {
		name  string
		owner string
		want  []string
	}{
		{
			name:  "user repo",
			owner: "",
			want:  []string{"Public", "Private"},
		},
		{
			name:  "org repo",
			owner: "fooOrg",
			want:  []string{"Public", "Private", "Internal"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getRepoVisibilityOptions(tt.owner))
		})
	}
}
