package settle

import (
	"os"
	"path/filepath"
	"strings"
)

// GitInfo describes the git checkout containing a directory. It is derived
// purely from the filesystem (no git binary) so the CLI stays dependency-free.
type GitInfo struct {
	Toplevel string // working-tree root (directory containing .git)
	Branch   string // branch name, or short commit SHA when detached, or ""
	// IsLinkedWorktree is true in a `git worktree add` checkout, where .git
	// is a file pointing into the main repository's .git/worktrees/<name>.
	IsLinkedWorktree bool
}

// FindGitInfo walks up from startDir to the enclosing git checkout.
// It returns ok=false when startDir is not inside a git repository.
func FindGitInfo(startDir string) (GitInfo, bool) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return GitInfo{}, false
	}
	for {
		gitPath := filepath.Join(dir, ".git")
		if fi, err := os.Stat(gitPath); err == nil {
			info := GitInfo{Toplevel: dir}
			gitDir := gitPath
			if !fi.IsDir() {
				// Linked worktree: .git is a file "gitdir: <path>".
				data, err := os.ReadFile(gitPath)
				if err != nil {
					return info, true
				}
				target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
				if !filepath.IsAbs(target) {
					target = filepath.Join(dir, target)
				}
				gitDir = target
				info.IsLinkedWorktree = strings.Contains(filepath.ToSlash(target), "/worktrees/")
			}
			info.Branch = readBranch(gitDir)
			return info, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return GitInfo{}, false
		}
		dir = parent
	}
}

// readBranch parses HEAD in a git dir: a symbolic ref yields the branch
// name, a detached HEAD yields the short commit SHA.
func readBranch(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	if ref, ok := strings.CutPrefix(head, "ref: "); ok {
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	if len(head) >= 12 {
		return head[:12]
	}
	return head
}
