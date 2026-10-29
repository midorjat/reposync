// Package repo captures, transfers and restores the full working state of a
// git repository: every local branch, the checked-out branch, and all
// uncommitted and untracked (non-ignored) files.
package repo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git runs git commands inside one repository.
type Git struct {
	Dir string
	Env []string
}

type GitError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *GitError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *GitError) Unwrap() error { return e.Err }

func (g Git) With(env ...string) Git {
	return Git{Dir: g.Dir, Env: append(append([]string{}, g.Env...), env...)}
}

func (g Git) Run(args ...string) (string, error) { return g.run(nil, args...) }

func (g Git) RunInput(input string, args ...string) (string, error) {
	return g.run(strings.NewReader(input), args...)
}

func (g Git) run(stdin io.Reader, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Env = append(cmd.Env, g.Env...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", &GitError{Args: args, Stderr: errb.String(), Err: err}
	}
	return strings.TrimRight(out.String(), "\r\n"), nil
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// gitPath resolves a path inside the repository's .git directory.
func (g Git) gitPath(name string) (string, error) {
	p, err := g.Run("rev-parse", "--git-path", name)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(g.Dir, p)
	}
	return p, nil
}

// dataDir is where reposync keeps its own files: .git/reposync.
func (g Git) dataDir() (string, error) {
	gd, err := g.Run("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	d := filepath.Join(gd, "reposync")
	return d, os.MkdirAll(d, 0o755)
}

// updateRefs applies all ref changes in one transaction. A nil sha deletes.
func (g Git) updateRefs(changes map[string]*string) error {
	if len(changes) == 0 {
		return nil
	}
	var b strings.Builder
	for ref, sha := range changes {
		if sha == nil {
			fmt.Fprintf(&b, "delete %s\n", ref)
		} else {
			fmt.Fprintf(&b, "update %s %s\n", ref, *sha)
		}
	}
	_, err := g.RunInput(b.String(), "update-ref", "--stdin")
	return err
}

// listRefs returns refname -> sha for every ref under prefix.
func (g Git) listRefs(prefix string) (map[string]string, error) {
	out, err := g.Run("for-each-ref", "--format=%(objectname) %(refname)", prefix)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if sha, ref, ok := strings.Cut(line, " "); ok {
			refs[ref] = sha
		}
	}
	return refs, nil
}

// replaceRefs makes the set of refs under prefix exactly refs.
func (g Git) replaceRefs(prefix string, refs map[string]string) error {
	existing, err := g.listRefs(prefix)
	if err != nil {
		return err
	}
	changes := map[string]*string{}
	for ref := range existing {
		if _, keep := refs[ref]; !keep {
			changes[ref] = nil
		}
	}
	for ref, sha := range refs {
		if existing[ref] != sha {
			changes[ref] = &sha
		}
	}
	return g.updateRefs(changes)
}

// Internal commits (snapshots) must not depend on the user's git identity.
var identityEnv = []string{
	"GIT_AUTHOR_NAME=reposync", "GIT_AUTHOR_EMAIL=reposync@localhost",
	"GIT_COMMITTER_NAME=reposync", "GIT_COMMITTER_EMAIL=reposync@localhost",
}

func tempFile(dir, pattern string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	return name, f.Close()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
