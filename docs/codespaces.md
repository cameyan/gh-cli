# 使用 CLI 操作 Codespaces 指南

有关 Codespaces 的更多信息，参见 [GitHub 文档中的 Codespaces 部分](https://docs.github.com/en/codespaces)。

## 访问其他仓库

codespace 创建过程会在创建时提示你审阅并授权 `devcontainer.json` 中定义的额外权限：

```json
{
  "customizations": {
    "codespaces": {
      "repositories": {
        "my_org/my_repo": {
          "permissions": {
            "issues": "write"
          }
        }
      }
    }
  }
}
```

不过，对 `codespaces` 自定义项的任何更改都不会针对现有 codespace 重新评估。因此，
你需要使用 `gh codespace create` 创建一个新的 codespace，以授权这些新权限。

更多信息参见[“仓库访问”](https://docs.github.com/en/codespaces/managing-your-codespaces/managing-repository-access-for-your-codespaces)。

如果现有 codespace 需要额外访问权限，或者需要访问你的用户账户或组织账户之外的仓库，
可以考虑将细粒度个人访问令牌用作环境变量或 Codespaces secret。

更多信息参见[“向仓库进行身份验证”](https://docs.github.com/en/codespaces/troubleshooting/troubleshooting-authentication-to-a-repository)。
