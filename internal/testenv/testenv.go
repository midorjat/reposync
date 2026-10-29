// Package testenv simulates machines for tests: each has its own clone of a
// shared origin, a Node, and a running agent.
package testenv

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

const Token = "test-token"

type Machine struct {
	T    testing.TB
	Name string
	Dir  string
	Node *node.Node
	Srv  *httptest.Server
}

func NewMachine(t testing.TB, name, origin string) *Machine {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	Git(t, root, "clone", "-q", origin, dir)
	cfg := &config.Config{Host: name, Token: Token, Root: root,
		Repos: []config.Repo{{Name: "app", Path: "app"}}}
	n := node.New(cfg)
	srv := httptest.NewServer(agent.NewServer(n, Token, log.New(io.Discard, "", 0)))
	t.Cleanup(srv.Close)
	return &Machine{T: t, Name: name, Dir: dir, Node: n, Srv: srv}
}

// Dial connects m to other, like the CLI does.
func (m *Machine) Dial(other *Machine) func(context.Context) (*syncer.Syncer, error) {
	return func(ctx context.Context) (*syncer.Syncer, error) {
		return syncer.New(ctx, m.Node, agent.NewClient(other.Srv.URL, Token))
	}
}

// SyncWith runs one sync of "app" from m with other.
func (m *Machine) SyncWith(other *Machine, take syncer.Side) (syncer.Action, string) {
	m.T.Helper()
	ctx := context.Background()
	s, err := m.Dial(other)(ctx)
	if err != nil {
		m.T.Fatal(err)
	}
	p, msg, err := s.Sync(ctx, "app", take)
	if err != nil {
		m.T.Fatalf("sync %s->%s (%s): %v", m.Name, other.Name, p.Action, err)
	}
	return p.Action, msg
}

func (m *Machine) Write(path, content string) {
	m.T.Helper()
	p := filepath.Join(m.Dir, path)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		m.T.Fatal(err)
	}
}

func (m *Machine) Read(path string) string {
	data, err := os.ReadFile(filepath.Join(m.Dir, path))
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

func (m *Machine) Remove(path string) { os.Remove(filepath.Join(m.Dir, path)) }

func (m *Machine) Git(args ...string) string { m.T.Helper(); return Git(m.T, m.Dir, args...) }

func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@x", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// NewOrigin makes a repo with README.md, old.txt and a .gitignore for *.log.
func NewOrigin(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	Git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644)
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func Expect(t testing.TB, what string, got, want any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %q, want %q", what, got, want)
	}
}
