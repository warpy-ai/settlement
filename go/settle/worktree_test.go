package settle

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFindGitInfoMainCheckout(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/master\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	info, ok := FindGitInfo(nested)
	if !ok {
		t.Fatal("expected to find git info")
	}
	if info.IsLinkedWorktree {
		t.Error("main checkout misdetected as linked worktree")
	}
	if info.Branch != "master" {
		t.Errorf("branch = %q, want master", info.Branch)
	}
	if info.Toplevel != dir {
		t.Errorf("toplevel = %q, want %q", info.Toplevel, dir)
	}
}

func TestFindGitInfoLinkedWorktree(t *testing.T) {
	base := t.TempDir()
	// Simulate the real layout: main repo with .git/worktrees/task-a, and a
	// worktree checkout whose .git is a file pointing at it.
	wtGitDir := filepath.Join(base, "main", ".git", "worktrees", "task-a")
	if err := os.MkdirAll(wtGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtGitDir, "HEAD"), []byte("ref: refs/heads/task-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(base, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+wtGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	info, ok := FindGitInfo(wt)
	if !ok {
		t.Fatal("expected to find git info")
	}
	if !info.IsLinkedWorktree {
		t.Error("linked worktree not detected")
	}
	if info.Branch != "task-a" {
		t.Errorf("branch = %q, want task-a", info.Branch)
	}
}

func TestFindGitInfoDetachedHead(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(sha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, ok := FindGitInfo(dir)
	if !ok {
		t.Fatal("expected to find git info")
	}
	if info.Branch != sha[:12] {
		t.Errorf("branch = %q, want short sha %q", info.Branch, sha[:12])
	}
}

func TestFindGitInfoOutsideRepo(t *testing.T) {
	if _, ok := FindGitInfo(t.TempDir()); ok {
		t.Error("expected no git info outside a repository")
	}
}

// TestFindGitInfoRealWorktree exercises the real `git worktree add` layout
// end-to-end when the git binary is available.
func TestFindGitInfoRealWorktree(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	main := filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(main, "init", "-b", "master")
	if err := os.WriteFile(filepath.Join(main, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(main, "add", ".")
	run(main, "commit", "-m", "init")
	wt := filepath.Join(base, "wt")
	run(main, "worktree", "add", "-b", "task-a", wt)

	mainInfo, ok := FindGitInfo(main)
	if !ok || mainInfo.IsLinkedWorktree || mainInfo.Branch != "master" {
		t.Errorf("main checkout info = %+v, ok=%v", mainInfo, ok)
	}
	wtInfo, ok := FindGitInfo(wt)
	if !ok || !wtInfo.IsLinkedWorktree || wtInfo.Branch != "task-a" {
		t.Errorf("worktree info = %+v, ok=%v", wtInfo, ok)
	}
}

func TestInitWritesUnionMergeAttribute(t *testing.T) {
	store := initStore(t)
	data, err := os.ReadFile(filepath.Join(store.Root, ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "decisions.jsonl merge=union\n" {
		t.Errorf("gitattributes = %q", data)
	}
}
