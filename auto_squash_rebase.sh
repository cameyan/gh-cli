#!/usr/bin/env bash
set -e

UPSTREAM="https://github.com/cli/cli.git"

# 定义状态存储文件路径（用于保存 --continue 时需要的上下文参数）
STATUS_FILE=".git/auto_squash_rebase_status"

# 自定义颜色输出函数
out() {
    local color_code=""
    case "$1" in
        r|R) color_code="31" ;; # 红
        g|G) color_code="32" ;; # 绿
        y|Y) color_code="33" ;; # 黄
        b|B) color_code="34" ;; # 蓝
        m|M) color_code="35" ;; # 紫
        c|C) color_code="36" ;; # 青
        w|W) color_code="37" ;; # 白
        h|H) color_code="30" ;; # 黑
        *)   color_code="0"  ;; # 默认无颜色
    esac

    # 使用 printf 保证跨平台（Bash/Zsh）行为一致，输出后自动换行并重置颜色
    printf "\033[%sm%s\033[0m\n" "$color_code" "$2"
}

# 打印帮助信息的函数
show_help() {
    out "B" "================================================================================================="
    out "M" "                             💡 汉化分支自动压缩与变基升级工具"
    out "B" "================================================================================================="
    out "R" "用法示例:"
    out "C" "  1. 首次执行升级流程:"
    out "C" "     bash $(basename $0) --old <旧tag> --new <新tag> --hash <旧tag名称|提交Hash>"
    out "G" "     bash $(basename $0) --old v0.7.3 --new v0.7.4 --hash v0.7.3"
    out " " ""
    out "C" "  2. 解决冲突后继续执行:"
    out "G" "     bash $(basename $0) --continue"
    out " " ""
    out "C" "  3. 查看帮助:"
    out "G" "     bash $(basename $0) -h|--help"
    out "B" "================================================================================================="
}

# 解析命令行参数
OLD_TAG=""
NEW_TAG=""
HASH=""
IS_CONTINUE=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --old)
            OLD_TAG="$2"
            shift 2
            ;;
        --new)
            NEW_TAG="$2"
            shift 2
            ;;
        --hash)
            HASH="$2"
            shift 2
            ;;
        --continue)
            IS_CONTINUE=true
            shift
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            out "R" "❌ 错误: 未知参数 $1"
            show_help
            exit 1
            ;;
    esac
done

# 首先让用户确认上游仓库地址是否正确
printf "\033[33m当前脚本配置上游仓库: \033[31m$UPSTREAM\033[33m，是否正确(yes): \033[0m" && read -r user_input
# 严格匹配 'yes'
if [ "$user_input" != "yes" ]; then
    out "R" "用户已拒绝，脚本退出"
    exit 1
fi

# =================================================================
# 流程二：--continue 续接模式
# =================================================================
if [ "$IS_CONTINUE" = true ]; then
    # 1. 检查是否存在 Git 变基冲突状态
    if [ -d ".git/rebase-merge" ] || [ -d ".git/rebase-apply" ]; then
        out "M" "🔀 检测到仍处于变基流程中，正在尝试继续变基..."
        out "Y" "⚠️ 注意: 如果依然有冲突，请处理完冲突并 git add 后再次执行 --continue"

        # 暂时关闭遇错即停，因为继续变基如果还遇到冲突会返回非0状态码
        set +e
        # 暂不弹窗 提交信息窗口，后续 --amend 修改
        GIT_EDITOR=true git rebase --continue
        REBASE_STATUS=$?
        set -e

        if [ $REBASE_STATUS -ne 0 ]; then
            out "Y" "⚠️ 仍有冲突未解决，请继续修复，然后 git add <file> 并再次运行 --continue"
            exit 1
        fi
    fi

    # 2. 从状态文件中读取上下文参数
    if [ ! -f "$STATUS_FILE" ]; then
        out "R" "❌ 错误: 找不到变基断点状态文件 ${STATUS_FILE}，无法继续。请重新进行全新升级"
        exit 1
    fi

    # 读取保存的参数
    source "$STATUS_FILE"
    OLD_BRANCH="${OLD_TAG}-zh"
    NEW_BRANCH="${NEW_TAG}-zh"

    out "G" "💡 变基已成功通过验证！正在续接执行后续流程..."

# =================================================================
# 流程一：首次执行模式
# =================================================================
else
    # 基础参数校验
    if [ -z "$OLD_TAG" ] || [ -z "$NEW_TAG" ] || [ -z "$HASH" ]; then
        out "R" "❌ 错误: 缺少必要参数"
        show_help
        exit 1
    fi

    OLD_BRANCH="${OLD_TAG}-zh"
    NEW_BRANCH="${NEW_TAG}-zh"

    out "B" "=================================================="
    out "M" "🚀 开始执行自动化压缩与变基升级流程..."
    out "B" "=================================================="

    # 1. 检查并切回干净的旧汉化分支
    out "G" "🌿 正在切换并同步本地的旧汉化分支 $OLD_BRANCH ..."
    git checkout "$OLD_BRANCH" 2>/dev/null || git checkout -b "$OLD_BRANCH" "origin/$OLD_BRANCH"
    git reset --hard "origin/$OLD_BRANCH"

    # 2. 自动执行提交压缩 (Squash)
    SUBMIT_COUNT=$(git rev-list --count "$HASH"..HEAD)
    if [ "$SUBMIT_COUNT" -gt 1 ]; then
        # 备份防呆
        git branch "${OLD_BRANCH}_backup" 2>/dev/null || true

        out "G" "📦 正在将本地 $OLD_BRANCH 分支最近的 $SUBMIT_COUNT 笔自定义提交压缩为 1 笔..."
        # 软回退并重新提交
        git reset --soft "$HASH"
        git commit -m "压缩 $OLD_BRANCH 分支 $SUBMIT_COUNT 笔提交..."
        # 压缩成功，删除备份分支
        git branch -D "${OLD_BRANCH}_backup" >/dev/null 2>&1

        out "G" "✅ 本地 $OLD_BRANCH 分支自定义提交已成功压缩为 1 笔"
    else
        out "C" "⏭️  压缩笔数 -n 小于或等于 1，无需压缩，跳过压缩步骤"
    fi

    # 3. 保存当前运行上下文到状态文件（为可能的冲突断点做准备）
    echo "OLD_TAG=\"$OLD_TAG\"" > "$STATUS_FILE"
    echo "NEW_TAG=\"$NEW_TAG\"" >> "$STATUS_FILE"

    # 4. 配置上游源并同步标签
    if ! git remote | grep -q "upstream"; then
        git remote add upstream "$UPSTREAM"
    fi
    out "G" "📥 正在从 upstream 同步最新标签..."
    git fetch upstream --tags

    # 5. 执行变基操作
    out "G" "🔀 正在基于官方新标签 $NEW_TAG 执行变基操作..."
    set +e
    git rebase "$NEW_TAG"
    REBASE_STATUS=$?
    set -e

    # 冲突挂起处理
    if [ $REBASE_STATUS -ne 0 ]; then
        out "B" "=================================================="
        out "Y" "⚠️ 发现代码冲突，变基流程已暂停!!!"
        out "C" "👉 请按以下步骤处理:"
        out "G" "   1. 打开冲突文件，手动解决冲突并保存"
        out "G" "   2. 执行: git add <冲突文件>"
        out "G" "   3. 修复完毕后，请直接执行: bash $(basename $0) --continue"
        out "B" "=================================================="
        exit 1
    fi
fi

# =================================================================
# 公共收尾流程（全新成功，或 --continue 成功后都会走到这里）
# =================================================================
out "G" "✨ 变基状态确认 OK，正在创建并切换到新分支 $NEW_BRANCH ..."
git branch -D "$NEW_BRANCH" 2>/dev/null || true
git checkout -b "$NEW_BRANCH"

# 切换到变基后的新分支，强行重写之前压缩为 1 笔的提交信息
git commit --amend -m "变基到 $NEW_BRANCH 分支并从 $OLD_BRANCH 旧分支同步已汉化内容..."

out "G" "📤 正在将新分支 $NEW_BRANCH 推送到远程并建立追踪关系..."
git push -u origin "$NEW_BRANCH"

# 同步推送上游新标签到远程
out "G" "🏷️ 正在将上游标签 $NEW_TAG 推送到远程仓库..."
git push --force origin "$NEW_TAG"

out "G" "↩️  正在恢复本地的旧分支 $OLD_BRANCH 到原始状态..."
git checkout "$OLD_BRANCH"
git reset --hard "origin/$OLD_BRANCH"

# 最终切回新分支，方便开发
git checkout "$NEW_BRANCH"

# 清理断点状态文件
rm -f "$STATUS_FILE"

out "B" "=================================================="
out "G" "🎉 恭喜! 自动化压缩并变基升级流程已全部完成~"
out "G" "👉 当前已自动切换至新分支: $NEW_BRANCH"
out "B" "=================================================="
