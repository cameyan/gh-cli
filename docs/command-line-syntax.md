# 我们如何编写命令行语法的文档

## 字面文本

命令中不可更改的部分使用纯文本。

_示例：_
`gh help`
此命令中必须提供参数 help。

## 占位符值

使用尖括号表示用户必须替换的值。尖括号内不能包含其他表达式。

_示例：_
`gh pr view <issue-number>`
将 `<issue-number>` 替换为问题编号。

## 可选参数

将可选参数放在方括号中。互斥参数可以用竖线分隔后放入方括号内。

_示例：_
`gh pr checkout [--web]`
参数 `--web` 是可选的。

`gh pr view [<number> | <url>]`
`<number>` 和 `<url>` 参数是可选的。

## 必需的互斥参数

将必需的互斥参数放在花括号中，参数之间用竖线分隔。

_示例：_
`gh pr {view | create}`

## 可重复参数

省略号表示可以多次出现的参数。

_示例：_
`gh pr close <pr-number>...`

## 变量命名

多词变量使用 dash-case（全部小写，单词之间用短横线分隔）。

_示例：_
`gh pr checkout <issue-number>`

## 其他示例

_带占位符的可选参数：_
`command sub-command [<arg>]`

_带互斥选项的必需参数：_
`command sub-command {<path> | <string> | literal}`

_带互斥选项的可选参数：_
`command sub-command [<path> | <string>]`
