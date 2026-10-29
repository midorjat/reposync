package syncer_test

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"reposync/internal/agent"
	"reposync/internal/config"
	"reposync/internal/node"
	"reposync/internal/syncer"
)

const token = "test-token"

// machine is one simulated computer: a clone of the shared origin, a Node
// and the agent other machines talk to.
type machine struct {
	t    *testing.T
	name string
	dir  string
	node *node.Node
	srv  *httptest.Server
}

func newMachine(t *testing.T, name, origin string) *machine {
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	git(t, root, "clone", "-q", origin, dir)
	cfg := &config.Config{Host: name, Token: token, Root: root,
		Repos: []config.Repo{{Name: "app", Path: "app"}}}
	n := node.New(cfg)
	srv := httptest.NewServer(agent.NewServer(n, token, log.New(io.Discard, "", 0)))
	t.Cleanup(srv.Close)
	return &machine{t: t, name: name, dir: dir, node: n, srv: srv}
}

// syncWith runs a sync initiated from m against other and returns the message.
func (m *machine) syncWith(other *machine, take syncer.Side) (syncer.Action, string) {
	m.t.Helper()
	ctx := context.Background()
	s, err := syncer.New(ctx, m.node, agent.NewClient(other.srv.URL, token))
	if err != nil {
		m.t.Fatal(err)
	}
	p := s.Plan(ctx, "app", take)
	msg, err := s.Run(ctx, p)
	if err != nil {
		m.t.Fatalf("sync %s->%s (%s): %v", m.name, other.name, p.Action, err)
	}
	return p.Action, msg
}

func (m *machine) write(path, content string) {
	m.t.Helper()
	p := filepath.Join(m.dir, path)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) read(path string) string {
	data, err := os.ReadFile(filepath.Join(m.dir, path))
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

func (m *machine) git(args ...string) string { m.t.Helper(); return git(m.t, m.dir, args...) }

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@x", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newOrigin(t *testing.T) string {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func expect(t *testing.T, what string, got, want any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %q, want %q", what, got, want)
	}
}

func TestHandoffBothWays(t *testing.T) {
	origin := newOrigin(t)
	desktop := newMachine(t, "desktop", origin)
	laptop := newMachine(t, "laptop", origin)

	// Fresh clones are identical: in sync, and that becomes the base.
	act, _ := laptop.syncWith(desktop, syncer.None)
	expect(t, "fresh clones", act, syncer.InSync)

	// Work on the desktop: modify, add untracked, delete, and an ignored file.
	desktop.write("README.md", "hello from desktop\n")
	desktop.write("src/new.go", "package src\n")
	os.Remove(filepath.Join(desktop.dir, "old.txt"))
	desktop.write("debug.log", "ignored\n")
	desktop.git("add", "README.md") // staged, to check the real index is left alone by snapshots

	// Morning: the laptop pulls it all.
	act, msg := laptop.syncWith(desktop, syncer.None)
	expect(t, "laptop pulls ("+msg+")", act, syncer.Pull)
	expect(t, "README on laptop", laptop.read("README.md"), "hello from desktop\n")
	expect(t, "untracked file on laptop", laptop.read("src/new.go"), "package src\n")
	expect(t, "deleted file on laptop", laptop.read("old.txt"), "<missing>")
	expect(t, "ignored file not synced", laptop.read("debug.log"), "<missing>")
	expect(t, "desktop index untouched", desktop.git("diff", "--cached", "--name-only"), "README.md")

	// Nothing changed since: in sync both ways.
	act, _ = laptop.syncWith(desktop, syncer.None)
	expect(t, "after pull", act, syncer.InSync)
	act, _ = desktop.syncWith(laptop, syncer.None)
	expect(t, "after pull, from desktop", act, syncer.InSync)

	// At the office: commit on a new branch, keep some WIP on top.
	laptop.git("checkout", "-q", "-b", "feature")
	laptop.git("add", "-A")
	laptop.git("commit", "-q", "-m", "work from desktop")
	laptop.write("src/new.go", "package src\n\nfunc Office() {}\n")
	head := laptop.git("rev-parse", "HEAD")

	// Evening: the laptop pushes; the desktop continues exactly there.
	act, msg = laptop.syncWith(desktop, syncer.None)
	expect(t, "laptop pushes ("+msg+")", act, syncer.Push)
	expect(t, "desktop branch", desktop.git("branch", "--show-current"), "feature")
	expect(t, "desktop HEAD", desktop.git("rev-parse", "HEAD"), head)
	expect(t, "desktop WIP", desktop.read("src/new.go"), "package src\n\nfunc Office() {}\n")
	expect(t, "desktop status", desktop.git("status", "--porcelain"), "M src/new.go")
	expect(t, "desktop ignored file left alone", desktop.read("debug.log"), "ignored\n")

	// The desktop can start the next sync too: it edits, then pushes.
	desktop.write("README.md", "desktop again\n")
	act, _ = desktop.syncWith(laptop, syncer.None)
	expect(t, "desktop pushes", act, syncer.Push)
	expect(t, "laptop README", laptop.read("README.md"), "desktop again\n")
}

func TestConflictNeedsChoiceAndKeepsWork(t *testing.T) {
	origin := newOrigin(t)
	desktop := newMachine(t, "desktop", origin)
	laptop := newMachine(t, "laptop", origin)
	laptop.syncWith(desktop, syncer.None)

	desktop.write("README.md", "desktop version\n")
	laptop.write("README.md", "laptop version\n")

	act, msg := laptop.syncWith(desktop, syncer.None)
	expect(t, "plan", act, syncer.Conflict)
	if !strings.Contains(msg, "skipped") {
		t.Fatalf("conflict without --take should be skipped, got %q", msg)
	}
	expect(t, "desktop untouched", desktop.read("README.md"), "desktop version\n")
	expect(t, "laptop untouched", laptop.read("README.md"), "laptop version\n")

	// Laptop wins; the desktop's edit survives on a branch on both machines.
	act, msg = laptop.syncWith(desktop, syncer.LocalSide)
	expect(t, "take laptop ("+msg+")", act, syncer.Conflict)
	expect(t, "desktop now has laptop's file", desktop.read("README.md"), "laptop version\n")
	kept := desktop.git("for-each-ref", "--format=%(refname:short)", "refs/heads/sync/")
	if !strings.HasPrefix(kept, "sync/desktop/main-") {
		t.Fatalf("expected a sync/desktop/main-* branch, got %q", kept)
	}
	expect(t, "kept work content", desktop.git("show", kept+":README.md"), "desktop version")

	act, _ = desktop.syncWith(laptop, syncer.None)
	expect(t, "settled", act, syncer.InSync)

	// Undo on the desktop brings back what it had before.
	b, _, err := desktop.node.Undo("app", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID == "" {
		t.Fatal("no backup id")
	}
	expect(t, "undo restores", desktop.read("README.md"), "desktop version\n")
	act, _ = laptop.syncWith(desktop, syncer.None)
	expect(t, "after undo the next sync asks", act, syncer.Choose)
}

func TestFirstSyncWithDifferencesAsks(t *testing.T) {
	origin := newOrigin(t)
	desktop := newMachine(t, "desktop", origin)
	laptop := newMachine(t, "laptop", origin)
	desktop.write("wip.txt", "unsaved\n")

	act, _ := laptop.syncWith(desktop, syncer.None)
	expect(t, "first sync", act, syncer.Choose)
	expect(t, "laptop untouched", laptop.read("wip.txt"), "<missing>")

	act, _ = laptop.syncWith(desktop, syncer.PeerSide)
	expect(t, "take desktop", act, syncer.Choose)
	expect(t, "laptop got it", laptop.read("wip.txt"), "unsaved\n")
	// The laptop was a clean clone already on main: nothing to keep.
	expect(t, "no kept branch", laptop.git("branch", "--list", "sync/*"), "")
}
