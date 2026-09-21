#!/bin/bash
set -e

UPSTREAM="https://atomgit.com/cli/cli.git"

# 定义状态存储文件路径（用于保存 --continue 时需要的上下文参数）
STATUS_FILE=".git/auto_squash_rebase_status"

# 打印帮助信息的函数
show_help() {
    echo "==============================================================================================="
    echo "💡               汉化分支自动压缩与变基升级工具"
    echo "==============================================================================================="
    echo "用法示例:"
    echo "  1. 首次执行升级流程:"
    echo "     ./auto_squash_rebase.sh --old <旧标签> --new <新标签> --hash <分支名称|Tag名称|提交Hash>"
    echo "     示例: ./auto_squash_rebase.sh --old v0.7.3 --new v0.7.4 --hash main|v2.101.0|45437bc7"
    echo ""
    echo "  2. 解决冲突后继续执行:"
    echo "     ./auto_squash_rebase.sh --continue"
    echo ""
    echo "  3. 查看帮助:"
    echo "     ./auto_squash_rebase.sh -h | --help"
    echo "==============================================================================================="
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
            echo "❌ 错误: 未知参数 $1"
            show_help
            exit 1
            ;;
    esac
done

# =================================================================
# 流程二：--continue 续接模式
# =================================================================
if [ "$IS_CONTINUE" = true ]; then
    # 1. 检查是否存在 Git 变基冲突状态
    if [ -d ".git/rebase-merge" ] || [ -d ".git/rebase-apply" ]; then
        echo "🔀 检测到仍处于变基流程中，正在尝试继续变基..."
        echo "⚠️ 注意: 如果依然有冲突，请处理完冲突并 git add 后再次执行 --continue"

        # 暂时关闭遇错即停，因为继续变基如果还遇到冲突会返回非0状态码
        set +e
        # 暂不弹窗 提交信息窗口，后续 --amend 修改
        GIT_EDITOR=true git rebase --continue
        REBASE_STATUS=$?
        set -e

        if [ $REBASE_STATUS -ne 0 ]; then
            echo "⚠️ 仍有冲突未解决，请继续修复，然后 git add 并再次运行 --continue"
            exit 1
        fi
    fi

    # 2. 从状态文件中读取上下文参数
    if [ ! -f "$STATUS_FILE" ]; then
        echo "❌ 错误: 找不到变基断点状态文件，无法继续。请重新进行全新升级。"
        exit 1
    fi

    # 读取保存的参数
    source "$STATUS_FILE"
    OLD_BRANCH="${OLD_TAG}-zh"
    NEW_BRANCH="${NEW_TAG}-zh"

    echo "💡 变基已成功通过验证！正在续接执行后续流程..."

# =================================================================
# 流程一：首次执行模式
# =================================================================
else
    # 基础参数校验
    if [ -z "$OLD_TAG" ] || [ -z "$NEW_TAG" ] || [ -z "$HASH" ]; then
        echo "❌ 错误: 缺少必要参数！"
        show_help
        exit 1
    fi

    OLD_BRANCH="${OLD_TAG}-zh"
    NEW_BRANCH="${NEW_TAG}-zh"

    echo "=================================================="
    echo "🚀 开始执行自动化压缩与变基升级流程..."
    echo "=================================================="

    # 1. 检查并切回干净的旧汉化分支
    echo "🌿 正在切换并同步本地的旧汉化分支 $OLD_BRANCH ..."
    git checkout "$OLD_BRANCH" 2>/dev/null || git checkout -b "$OLD_BRANCH" "origin/$OLD_BRANCH"
    git reset --hard "origin/$OLD_BRANCH"

    # 2. 自动执行提交压缩 (Squash)
    SUBMIT_COUNT=$(git rev-list --count "$HASH"..HEAD)
    if [ "$SUBMIT_COUNT" -gt 1 ]; then
        # 备份防呆
        git branch "${OLD_BRANCH}_backup" 2>/dev/null || true

        echo "📦 正在将本地 $OLD_BRANCH 分支最近的 $SUBMIT_COUNT 笔自定义提交压缩为 1 笔..."
        # 软回退并重新提交
        git reset --soft "$HASH"
        git commit -m "压缩 $OLD_BRANCH 分支 $SUBMIT_COUNT 笔提交..."
        # 压缩成功，删除备份分支
        git branch -D "${OLD_BRANCH}_backup" >/dev/null 2>&1

        echo "✅ 本地 $OLD_BRANCH 分支自定义提交已成功压缩为 1 笔"
    else
        echo "⏭️  压缩笔数 -n 小于或等于 1，跳过压缩步骤"
    fi

    # 3. 保存当前运行上下文到状态文件（为可能的冲突断点做准备）
    echo "OLD_TAG=\"$OLD_TAG\"" > "$STATUS_FILE"
    echo "NEW_TAG=\"$NEW_TAG\"" >> "$STATUS_FILE"

    # 4. 配置上游源并同步标签
    if ! git remote | grep -q "upstream"; then
        git remote add upstream "$UPSTREAM"
    fi
    echo "📥 正在从 upstream 同步最新标签..."
    git fetch upstream --tags

    # 5. 执行变基操作
    echo "🔀 正在基于官方新标签 $NEW_TAG 执行变基操作..."
    set +e
    git rebase "$NEW_TAG"
    REBASE_STATUS=$?
    set -e

    # 冲突挂起处理
    if [ $REBASE_STATUS -ne 0 ]; then
        echo "=================================================="
        echo "⚠️ 发现代码冲突！变基流程已暂停！"
        echo "👉 请按以下步骤处理:"
        echo "   1. 打开冲突文件，手动解决冲突并保存。"
        echo "   2. 执行: git add <冲突文件>"
        echo "   3. 修复完毕后，请直接执行: ./auto_squash_rebase.sh --continue"
        echo "=================================================="
        exit 1
    fi
fi

# =================================================================
# 公共收尾流程（全新成功，或 --continue 成功后都会走到这里）
# =================================================================
echo "✨ 变基状态确认 OK！正在创建并切换到新分支 $NEW_BRANCH ..."
git branch -D "$NEW_BRANCH" 2>/dev/null || true
git checkout -b "$NEW_BRANCH"

# 切换到变基后的新分支，强行重写之前压缩为 1 笔的提交信息
git commit --amend -m "变基到 $NEW_BRANCH 分支并从 $OLD_BRANCH 旧分支同步已汉化内容..."

echo "📤 正在将新分支 $NEW_BRANCH 推送到远程并建立追踪关系..."
git push -u origin "$NEW_BRANCH"

echo "↩️  正在恢复本地的旧分支 $OLD_BRANCH 到原始状态..."
git checkout "$OLD_BRANCH"
git reset --hard "origin/$OLD_BRANCH"

# 最终切回新分支，方便开发
git checkout "$NEW_BRANCH"

# 清理断点状态文件
rm -f "$STATUS_FILE"

echo "=================================================="
echo "🎉 恭喜！智能压缩并变基升级流程已全部完成！"
echo "👉 当前已自动切换至新分支: $NEW_BRANCH"
echo "=================================================="
