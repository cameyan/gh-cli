package shared

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	ghContext "github.com/cli/cli/v2/context"
	"github.com/cli/cli/v2/git"

	"github.com/cli/cli/v2/internal/ghrepo"
	o "github.com/cli/cli/v2/pkg/option"
)

// QualifiedHeadRef 表示一个带可选 owner 的 git 分支(branch)，用作拉取请求(PR)的 head。
// 例如，在单个仓库内，PR 的 head ref 通常没有 owner，只有一个分支名；
// 而对于跨仓库的拉取请求，head ref 则带 owner 和分支名。
// 以字符串表示时写作 <owner>:<branch>。GitHub API 能够解析这种格式，
// 从而找到正确的 fork 仓库。
//
// 在代码的其他部分，你可能看到它被称为 HeadLabel。
type QualifiedHeadRef struct {
	owner      o.Option[string]
	branchName string
}

// NewQualifiedHeadRef 创建一个 QualifiedHeadRef。若 owner 提供的是空字符串，
// 它将被视为 None。
func NewQualifiedHeadRef(owner string, branchName string) QualifiedHeadRef {
	return QualifiedHeadRef{
		owner:      o.SomeIfNonZero(owner),
		branchName: branchName,
	}
}

func NewQualifiedHeadRefWithoutOwner(branchName string) QualifiedHeadRef {
	return QualifiedHeadRef{
		owner:      o.None[string](),
		branchName: branchName,
	}
}

// ParseQualifiedHeadRef 接受形如 <owner>:<branch> 或 <branch> 的字符串，
// 并返回一个 QualifiedHeadRef。若使用 <owner>:<branch> 形式，
// 则 owner 被设置为 <owner> 的值，分支名被设置为 <branch> 的值；
// 若使用 <branch> 形式，则 owner 被设为 None，分支名被设为 <branch> 的值。
//
// 这里不对 ref 的有效性做进一步的错误检查，因此不能假定该 ref
// 真的是一个有效的 ref，例如 "my~bad:ref?" 会产生一个无意义的结果。
func ParseQualifiedHeadRef(ref string) (QualifiedHeadRef, error) {
	if !strings.Contains(ref, ":") {
		return NewQualifiedHeadRefWithoutOwner(ref), nil
	}

	parts := strings.Split(ref, ":")
	if len(parts) != 2 {
		return QualifiedHeadRef{}, fmt.Errorf("无效的限定 head ref 格式 '%s'", ref)
	}

	return NewQualifiedHeadRef(parts[0], parts[1]), nil
}

// 不带 owner 的 QualifiedHeadRef 返回 <branch>，而带 owner 的 QualifiedHeadRef
// 返回 <owner>:<branch>。
func (r QualifiedHeadRef) String() string {
	if owner, present := r.owner.Value(); present {
		return fmt.Sprintf("%s:%s", owner, r.branchName)
	}
	return r.branchName
}

func (r QualifiedHeadRef) BranchName() string {
	return r.branchName
}

// PRFindRefs 表示从 API 查找拉取请求(PR)所需的数据。
type PRFindRefs struct {
	qualifiedHeadRef QualifiedHeadRef

	baseRepo ghrepo.Interface
	// baseBranchName 是一个可选的分支名，因为查找拉取请求并不需要它，
	// 只有在多个拉取请求包含相同 head ref 时才用于消歧。
	baseBranchName o.Option[string]
}

// QualifiedHeadRef 返回 head ref 的字符串形式，具体取决于 head ref
// 是否与基础 ref 位于同一仓库。若为同一仓库，则仅返回分支名；
// 若为不同仓库，则以 <owner>:<branch> 形式返回 owner 和分支名。
func (r PRFindRefs) QualifiedHeadRef() string {
	return r.qualifiedHeadRef.String()
}

func (r PRFindRefs) UnqualifiedHeadRef() string {
	return r.qualifiedHeadRef.BranchName()
}

// Matches 检查提供的 baseBranchName 和 headRef 是否与这些 ref 匹配。
// 它用于判断 API 返回的拉取请求(PR)。
func (r PRFindRefs) Matches(baseBranchName, qualifiedHeadRef string) bool {
	headMatches := qualifiedHeadRef == r.QualifiedHeadRef()
	baseMatches := r.baseBranchName.IsNone() || baseBranchName == r.baseBranchName.Unwrap()
	return headMatches && baseMatches
}

func (r PRFindRefs) BaseRepo() ghrepo.Interface {
	return r.baseRepo
}

type RemoteNameToRepoFn func(remoteName string) (ghrepo.Interface, error)

// PullRequestFindRefsResolver 询问 git 配置，尝试从本地分支名确定
// head 仓库和远端分支名。
type PullRequestFindRefsResolver struct {
	GitConfigClient    GitConfigClient
	RemoteNameToRepoFn RemoteNameToRepoFn
}

func NewPullRequestFindRefsResolver(gitConfigClient GitConfigClient, remotesFn func() (ghContext.Remotes, error)) PullRequestFindRefsResolver {
	return PullRequestFindRefsResolver{
		GitConfigClient:    gitConfigClient,
		RemoteNameToRepoFn: newRemoteNameToRepoFn(remotesFn),
	}
}

// ResolvePullRequestRefs 接受基础仓库、基础分支名和本地分支名，
// 并使用 git 配置确定 head 仓库和远端分支名。若无法从 git 中确定，
// 则将 head 仓库默认设为基础仓库。
func (r *PullRequestFindRefsResolver) ResolvePullRequestRefs(baseRepo ghrepo.Interface, baseBranchName, localBranchName string) (PRFindRefs, error) {
	if baseRepo == nil {
		return PRFindRefs{}, fmt.Errorf("没有基础仓库则无法执行拉取请求(PR) ref 解析")
	}

	if localBranchName == "" {
		return PRFindRefs{}, fmt.Errorf("没有本地分支名则无法执行拉取请求(PR) ref 解析")
	}

	headPRRef, err := TryDetermineDefaultPRHead(r.GitConfigClient, remoteToRepoResolver{r.RemoteNameToRepoFn}, localBranchName)
	if err != nil {
		return PRFindRefs{}, err
	}

	// 若 headRepo 已解析，直接把结果转换为 refs 并返回。
	if headRepo, present := headPRRef.Repo.Value(); present {
		qualifiedHeadRef := NewQualifiedHeadRefWithoutOwner(headPRRef.BranchName)
		if !ghrepo.IsSame(headRepo, baseRepo) {
			qualifiedHeadRef = NewQualifiedHeadRef(headRepo.RepoOwner(), headPRRef.BranchName)
		}

		return PRFindRefs{
			qualifiedHeadRef: qualifiedHeadRef,
			baseRepo:         baseRepo,
			baseBranchName:   o.SomeIfNonZero(baseBranchName),
		}, nil
	}

	// 若未找到 head 仓库，则默认为基础仓库
	return PRFindRefs{
		qualifiedHeadRef: NewQualifiedHeadRefWithoutOwner(headPRRef.BranchName),
		baseRepo:         baseRepo,
		baseBranchName:   o.SomeIfNonZero(baseBranchName),
	}, nil
}

// DefaultPRHead 是 defaultPushTarget 的近亲，但保存的不是基本的 git remote 信息，
// 而是以 `gh` 术语表示的已解析仓库。
//
// 由于我们可能无法为某个分支确定默认远端，已解析的仓库同样可能缺失。
type DefaultPRHead struct {
	Repo       o.Option[ghrepo.Interface]
	BranchName string
}

// TryDetermineDefaultPRHead 是 determineDefaultPushTarget 的轻量包装，
// 它尝试将存在的远端转换为已解析的仓库。若远端不存在，
// 则通过为 repo 返回 None 来告知调用方。
func TryDetermineDefaultPRHead(gitClient GitConfigClient, remoteToRepo remoteToRepoResolver, branch string) (DefaultPRHead, error) {
	pushTarget, err := tryDetermineDefaultPushTarget(gitClient, branch)
	if err != nil {
		return DefaultPRHead{}, err
	}

	// 若没有远端，通过返回 None 让调用方自行决定如何处理。
	if pushTarget.remote.IsNone() {
		return DefaultPRHead{
			Repo:       o.None[ghrepo.Interface](),
			BranchName: pushTarget.branchName,
		}, nil
	}

	repo, err := remoteToRepo.resolve(pushTarget.remote.Unwrap())
	if err != nil {
		return DefaultPRHead{}, err
	}

	return DefaultPRHead{
		Repo:       o.Some(repo),
		BranchName: pushTarget.branchName,
	}, nil
}

// remote 表示某个分支 git 配置中 remote 键的值。
// 该值可能是名称或 URL，二者都是字符串，但遗憾的是 ReadBranchConfig
// 将它们解析到不同的字段中，容易意外制造出非法状态。
// 这是为了表明二者互斥而做的一种尝试。
type remote interface{ sealedRemote() }

type remoteName struct{ name string }

func (rn remoteName) sealedRemote() {}

type remoteURL struct{ url *url.URL }

func (ru remoteURL) sealedRemote() {}

// newRemoteNameToRepoFn 接受一个返回远端列表的函数，
// 并返回一个接受远端名称、返回对应仓库的函数。
// 这是一个便捷函数，避免调用方重复编写相同逻辑。
func newRemoteNameToRepoFn(remotesFn func() (ghContext.Remotes, error)) RemoteNameToRepoFn {
	return func(remoteName string) (ghrepo.Interface, error) {
		remotes, err := remotesFn()
		if err != nil {
			return nil, err
		}
		repo, err := remotes.FindByName(remoteName)
		if err != nil {
			return nil, err
		}
		return repo, nil
	}
}

// remoteToRepoResolver 提供一个工具方法，将远端(名称或 URL)解析为仓库(ghrepo.Interface)。
type remoteToRepoResolver struct {
	remoteNameToRepo RemoteNameToRepoFn
}

func NewRemoteToRepoResolver(remotesFn func() (ghContext.Remotes, error)) remoteToRepoResolver {
	return remoteToRepoResolver{
		remoteNameToRepo: newRemoteNameToRepoFn(remotesFn),
	}
}

// resolve 接受一个远端，返回代表它的仓库。
func (r remoteToRepoResolver) resolve(remote remote) (ghrepo.Interface, error) {
	switch v := remote.(type) {
	case remoteName:
		repo, err := r.remoteNameToRepo(v.name)
		if err != nil {
			return nil, fmt.Errorf("无法解析远端 %q: %w", v.name, err)
		}
		return repo, nil
	case remoteURL:
		repo, err := ghrepo.FromURL(v.url)
		if err != nil {
			return nil, fmt.Errorf("无法解析远端 URL %q: %w", v.url, err)
		}
		return repo, nil
	default:
		return nil, fmt.Errorf("不支持的远端类型 %T，值: %v", v, remote)
	}
}

// defaultPushTarget 表示远端名称或 URL 与分支名的组合，即不带额外参数运行
// `git push` 时我们预期该分支被推送到的位置。这是 PR 的 head 最可能出现的地方，
// 但并不保证如此。用户可能通过 `git push <remote> <local>:<remote>` 直接推送到
// 另一个分支，而没有设置跟踪(tracking)信息。分支名始终存在。
//
// 我们也可能无法确定远端，例如用户直接推送到某个 URL (`git push <url> <branch>`)，
// 这就是它可选的原因。远端存在时，可能是名称或 URL。
type defaultPushTarget struct {
	remote     o.Option[remote]
	branchName string
}

// newDefaultPushTarget 是 defaultPushTarget 的轻量包装，用于辅助泛型类型推断，
// 减少重复书写参数化类型的冗长。
func newDefaultPushTarget(remote remote, branchName string) defaultPushTarget {
	return defaultPushTarget{
		remote:     o.Some(remote),
		branchName: branchName,
	}
}

// tryDetermineDefaultPushTarget 使用 git 配置对分支被推送到的位置、
// 以及用户不带额外参数运行 `git push` 时将被推送到的位置做出最佳猜测。
//
// 首先，它尝试解析 @{push} ref，这是最可靠的方法，
// 因为 git 正是用它来确定远端跟踪分支的。
//
// 若失败，我们通过一系列步骤确定远端:
//
// 1. 检查分支配置中的 `branch.<name>.pushRemote = <name> | <url>`
// 2. 检查远端配置中的 `remote.pushDefault = <name>`
// 3. 检查分支配置中的 `branch.<name>.remote = <name> | <url>`
//
// 若均未设置，则通过为远端返回 None 来表明我们无法确定远端。
//
// 分支名始终会被设置。push.default 的默认配置(current)表明 git push 应使用
// 与本地分支名相同的远端分支名。若 push.default 被设为 upstream 或 tracking
// (upstream 的废弃形式)，则使用 merge ref 中的分支名。
func tryDetermineDefaultPushTarget(gitClient GitConfigClient, localBranchName string) (defaultPushTarget, error) {
	// 若 @{push} 能解析，则我们已拥有远端跟踪分支，没有问题。
	if pushRevisionRef, err := gitClient.PushRevision(context.Background(), localBranchName); err == nil {
		return newDefaultPushTarget(remoteName{pushRevisionRef.Remote}, pushRevisionRef.Branch), nil
	}

	// 但它并不总能解析，因此我们可以抑制该错误并转用其他确定方式。
	// 我们先查看分支和远端配置来做判断。
	branchConfig, err := gitClient.ReadBranchConfig(context.Background(), localBranchName)
	if err != nil {
		return defaultPushTarget{}, err
	}

	pushDefault, err := gitClient.PushDefault(context.Background())
	if err != nil {
		return defaultPushTarget{}, err
	}

	// 我们假定 PR 的分支名与提供的一致，除非用户指定了 push.default = upstream
	// 或 tracking，此时若 merge ref 存在则使用其中的分支名。否则回退到本地分支名。
	remoteBranch := localBranchName
	if pushDefault == git.PushDefaultUpstream || pushDefault == git.PushDefaultTracking {
		mergeRef := strings.TrimPrefix(branchConfig.MergeRef, "refs/heads/")
		if mergeRef != "" {
			remoteBranch = mergeRef
		}
	}

	// 获取远端时，我们查看 git 配置。它按优先级来自以下之一:
	// 1. branch.<name>.pushRemote (可能是名称或 URL)
	// 2. remote.pushDefault (是远端名称)
	// 3. branch.<name>.remote (可能是名称或 URL)
	if branchConfig.PushRemoteName != "" {
		return newDefaultPushTarget(
			remoteName{branchConfig.PushRemoteName},
			remoteBranch,
		), nil
	}

	if branchConfig.PushRemoteURL != nil {
		return newDefaultPushTarget(
			remoteURL{branchConfig.PushRemoteURL},
			remoteBranch,
		), nil
	}

	remotePushDefault, err := gitClient.RemotePushDefault(context.Background())
	if err != nil {
		return defaultPushTarget{}, err
	}

	if remotePushDefault != "" {
		return newDefaultPushTarget(
			remoteName{remotePushDefault},
			remoteBranch,
		), nil
	}

	if branchConfig.RemoteName != "" {
		return newDefaultPushTarget(
			remoteName{branchConfig.RemoteName},
			remoteBranch,
		), nil
	}

	if branchConfig.RemoteURL != nil {
		return newDefaultPushTarget(
			remoteURL{branchConfig.RemoteURL},
			remoteBranch,
		), nil
	}

	// 若找不到远端，则通过 None 告知调用方。
	return defaultPushTarget{
		remote:     o.None[remote](),
		branchName: remoteBranch,
	}, nil
}
