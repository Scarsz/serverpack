package srcbuild

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// IsFullSHA reports whether ref is a full commit hash (SHA-1 or SHA-256).
func IsFullSHA(ref string) bool { return fullSHA.MatchString(ref) }

// git runs a git command and returns its stdout, including stderr in errors.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errNoGit
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0") // never hang on a credentials prompt
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}

// LsRemote resolves ref (a branch, tag, HEAD or full commit hash) in the repository at url to a commit.
func LsRemote(ctx context.Context, url, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	if IsFullSHA(ref) {
		return ref, nil
	}
	// Ask for the peeled pattern too: annotated tags only list their commit under "<tag>^{}".
	out, err := git(ctx, "", "ls-remote", url, ref, ref+"^{}")
	if err != nil {
		return "", err
	}
	// Exact matches only (ls-remote matches ref patterns by suffix); annotated tags resolve via their ^{} line.
	wanted := map[string]bool{ref: true, "refs/heads/" + ref: true, "refs/tags/" + ref: true}
	commits := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		peeled := strings.HasSuffix(name, "^{}")
		name = strings.TrimSuffix(name, "^{}")
		if !wanted[name] {
			continue
		}
		if _, seen := commits[name]; !seen || peeled {
			commits[name] = sha
		}
	}
	distinct := map[string]bool{}
	for _, sha := range commits {
		distinct[sha] = true
	}
	switch len(distinct) {
	case 0:
		if regexp.MustCompile(`^[0-9a-f]{7,39}$`).MatchString(ref) {
			return "", fmt.Errorf("ref %q looks like an abbreviated commit; use the full hash", ref)
		}
		return "", fmt.Errorf("ref %q not found in %s", ref, url)
	case 1:
		for sha := range distinct {
			return sha, nil
		}
	}
	return "", fmt.Errorf("ref %q is ambiguous in %s (both a branch and a tag); use refs/heads/%s or refs/tags/%s", ref, url, ref, ref)
}

// checkout makes rev available in a temporary worktree of a cached clone of url.
func (b *Builder) checkout(ctx context.Context, url, rev string) (string, func(), error) {
	sum := sha256.Sum256([]byte(url))
	repo := filepath.Join(b.Cache.Dir, "git", hex.EncodeToString(sum[:])[:16])
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		os.RemoveAll(repo)
		if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
			return "", nil, err
		}
		fmt.Fprintf(b.stderr(), "cloning %s\n", url)
		if _, err := git(ctx, "", "clone", "--quiet", "--no-checkout", url, repo); err != nil {
			return "", nil, err
		}
	}
	has := func() bool {
		_, err := git(ctx, repo, "cat-file", "-e", rev+"^{commit}")
		return err == nil
	}
	if !has() {
		if _, err := git(ctx, repo, "fetch", "--quiet", "--tags", "origin"); err != nil {
			return "", nil, err
		}
		if !has() {
			// Commits that no branch or tag points at anymore can sometimes still be fetched directly.
			git(ctx, repo, "fetch", "--quiet", "origin", rev)
			if !has() {
				return "", nil, fmt.Errorf("commit %s is not in %s", rev, url)
			}
		}
	}

	suffix := make([]byte, 6)
	rand.Read(suffix)
	wt := filepath.Join(b.Cache.Dir, "build-work", hex.EncodeToString(suffix))
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return "", nil, err
	}
	if _, err := git(ctx, repo, "worktree", "add", "--quiet", "--detach", "--force", wt, rev); err != nil {
		return "", nil, err
	}
	cleanup := func() {
		git(context.Background(), repo, "worktree", "remove", "--force", wt)
		os.RemoveAll(wt)
		git(context.Background(), repo, "worktree", "prune")
	}
	return wt, cleanup, nil
}
