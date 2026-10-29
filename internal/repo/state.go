package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	selfPrefix   = "refs/reposync/self/"
	peersPrefix  = "refs/reposync/peers/"
	backupPrefix = "refs/reposync/backup/"
)

var (
	ErrNoCommits = errors.New("repository has no commits yet")
	ErrBusy      = errors.New("a merge, rebase or cherry-pick is in progress; finish it first")
	ErrChanged   = errors.New("files changed while syncing; run sync again")
)

// State is everything needed to recreate a working copy on another machine.
type State struct {
	Host     string            `json:"host"`
	Time     time.Time         `json:"time"`
	Branch   string            `json:"branch,omitempty"` // empty when HEAD is detached
	Head     string            `json:"head"`
	HeadTree string            `json:"headTree"`
	WipTree  string            `json:"wipTree"` // HEAD plus every uncommitted and untracked file
	Branches map[string]string `json:"branches"`

	// Commit is the snapshot commit holding WipTree, once one was written.
	Commit string `json:"-"`
}

// Fingerprint identifies the content of a state, ignoring who took it and when.
func (s State) Fingerprint() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n", s.Branch, s.Head, s.WipTree)
	names := make([]string, 0, len(s.Branches))
	for n := range s.Branches {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(h, "%s %s\n", n, s.Branches[n])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (s State) Dirty() bool { return s.WipTree != s.HeadTree }

// Label is a short human description, e.g. "main* @1a2b3c4".
func (s State) Label() string {
	b := s.Branch
	if b == "" {
		b = "(detached)"
	}
	if s.Dirty() {
		b += "*"
	}
	return fmt.Sprintf("%s @%s", b, short(s.Head))
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Capture reads the current state of the repository without changing it.
func Capture(dir, host string) (State, error) {
	g := Git{Dir: dir}
	if err := checkIdle(g); err != nil {
		return State{}, err
	}
	head, err := g.Run("rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		if exitCode(err) == 1 {
			return State{}, ErrNoCommits
		}
		return State{}, err
	}
	s := State{Host: host, Time: time.Now().UTC(), Head: head, Branches: map[string]string{}}
	if s.HeadTree, err = g.Run("rev-parse", head+"^{tree}"); err != nil {
		return State{}, err
	}
	if s.Branch, err = g.Run("symbolic-ref", "-q", "--short", "HEAD"); err != nil {
		if exitCode(err) != 1 {
			return State{}, err
		}
		s.Branch = ""
	}
	refs, err := g.listRefs("refs/heads/")
	if err != nil {
		return State{}, err
	}
	for ref, sha := range refs {
		s.Branches[strings.TrimPrefix(ref, "refs/heads/")] = sha
	}
	if s.WipTree, err = wipTree(g); err != nil {
		return State{}, err
	}
	return s, nil
}

func checkIdle(g Git) error {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		p, err := g.gitPath(name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(p); err == nil {
			return ErrBusy
		}
	}
	return nil
}

// wipTree stages everything into a throwaway index and writes it as a tree,
// leaving the real index (and so what the user has staged) untouched.
// Starting from a copy of the real index keeps git's stat cache, so only
// changed files get hashed.
func wipTree(g Git) (string, error) {
	dd, err := g.dataDir()
	if err != nil {
		return "", err
	}
	tmp, err := tempFile(dd, "index-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	ig := g.With("GIT_INDEX_FILE=" + tmp)

	idx, err := g.gitPath("index")
	if err != nil {
		return "", err
	}
	if err := copyFile(idx, tmp); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		os.Remove(tmp) // read-tree refuses to load an empty file as an index
		if _, err := ig.Run("read-tree", "HEAD"); err != nil {
			return "", err
		}
	}
	if _, err := ig.Run("add", "-A"); err != nil {
		return "", err
	}
	return ig.Run("write-tree")
}

// writeStateCommit records s as a commit: tree = WipTree, parent = HEAD, and
// the state itself as JSON in the message. That makes a snapshot a normal
// git object that bundles, backups and `git show` all understand.
func writeStateCommit(g Git, s State) (string, error) {
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("reposync: work in progress from %s on %s\n\n%s\n", s.Host, s.Label(), body)
	return g.With(identityEnv...).RunInput(msg, "commit-tree", s.WipTree, "-p", s.Head, "-F", "-")
}

func readStateCommit(g Git, rev string) (State, error) {
	sha, err := g.Run("rev-parse", "--verify", "-q", rev+"^{commit}")
	if err != nil {
		return State{}, fmt.Errorf("no snapshot at %s", rev)
	}
	msg, err := g.Run("log", "-1", "--format=%B", sha)
	if err != nil {
		return State{}, err
	}
	i := strings.Index(msg, "\n{")
	if i < 0 {
		return State{}, fmt.Errorf("%s is not a reposync snapshot", rev)
	}
	var s State
	if err := json.Unmarshal([]byte(msg[i+1:]), &s); err != nil {
		return State{}, fmt.Errorf("%s: bad snapshot: %w", rev, err)
	}
	s.Commit = sha
	return s, nil
}

// writeSnapshotRefs stores a snapshot as <prefix>state plus <prefix>heads/*,
// which keeps every object it needs reachable.
func writeSnapshotRefs(g Git, prefix string, s State) error {
	refs := map[string]string{prefix + "state": s.Commit}
	for b, sha := range s.Branches {
		refs[prefix+"heads/"+b] = sha
	}
	return g.replaceRefs(prefix, refs)
}
