// Package node runs repo operations on this machine by repo name. The same
// operations are served to the peer by the agent, so a Node and an
// agent.Client both satisfy Peer.
package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"reposync/internal/config"
	"reposync/internal/repo"
)

const Version = "0.1.0"

var (
	ErrUnknownRepo = errors.New("repo is not in this machine's config")
	ErrMissing     = errors.New("repo is not cloned on this machine (run `reposync bootstrap`)")
)

type Hello struct {
	Host    string `json:"host"`
	Version string `json:"version"`
}

// Peer is the set of operations one machine asks of the other.
type Peer interface {
	Hello(ctx context.Context) (Hello, error)
	State(ctx context.Context, name string) (repo.State, error)
	Export(ctx context.Context, name string, w io.Writer) error
	Import(ctx context.Context, name, from string, r io.Reader) (repo.State, error)
	Apply(ctx context.Context, name, from, expect string) (repo.State, error)
	KeepWork(ctx context.Context, name, from string) (string, error)
	Base(ctx context.Context, name, peer string) (*repo.Base, error)
	SetBase(ctx context.Context, name, peer string, b repo.Base) error
}

type Node struct {
	Cfg *config.Config
	// git dislikes concurrent writers, and the agent may be asked to work
	// on a repo while a local command is running; serialize everything.
	mu sync.Mutex
}

func New(cfg *config.Config) *Node { return &Node{Cfg: cfg} }

func (n *Node) Dir(name string) (string, error) {
	r, ok := n.Cfg.Repo(name)
	if !ok {
		return "", fmt.Errorf("%s: %w", name, ErrUnknownRepo)
	}
	dir := n.Cfg.RepoDir(r)
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("%s: %w", name, ErrMissing)
	}
	return dir, nil
}

func (n *Node) Hello(context.Context) (Hello, error) {
	return Hello{Host: n.Cfg.Host, Version: Version}, nil
}

func (n *Node) State(_ context.Context, name string) (repo.State, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	dir, err := n.Dir(name)
	if err != nil {
		return repo.State{}, err
	}
	return repo.Capture(dir, n.Cfg.Host)
}

func (n *Node) Export(_ context.Context, name string, w io.Writer) error {
	path, err := n.exportBundle(name)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// exportBundle holds the lock only while the bundle is made, not while it is sent.
func (n *Node) exportBundle(name string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	dir, err := n.Dir(name)
	if err != nil {
		return "", err
	}
	_, path, err := repo.ExportBundle(dir, n.Cfg.Host)
	return path, err
}

func (n *Node) Import(_ context.Context, name, from string, r io.Reader) (repo.State, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	dir, err := n.Dir(name)
	if err != nil {
		return repo.State{}, err
	}
	return repo.Import(dir, from, r)
}

func (n *Node) Apply(_ context.Context, name, from, expect string) (repo.State, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	dir, err := n.Dir(name)
	if err != nil {
		return repo.State{}, err
	}
	target, err := repo.Imported(dir, from)
	if err != nil {
		return repo.State{}, err
	}
	return repo.Apply(dir, n.Cfg.Host, target, expect)
}

func (n *Node) KeepWork(_ context.Context, name, from string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	dir, err := n.Dir(name)
	if err != nil {
		return "", err
	}
	return repo.KeepWork(dir, from)
}

func (n *Node) SetBase(_ context.Context, name, peer string, b repo.Base) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	dir, err := n.Dir(name)
	if err != nil {
		return err
	}
	return repo.SaveBase(dir, peer, b)
}

func (n *Node) Base(_ context.Context, name, peer string) (*repo.Base, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	dir, err := n.Dir(name)
	if err != nil {
		return nil, err
	}
	return repo.LoadBase(dir, peer)
}

// Undo restores a backup and forgets the sync record, so the next sync
// asks which side should win instead of pushing the restored state.
func (n *Node) Undo(name, id string) (repo.Backup, repo.State, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	dir, err := n.Dir(name)
	if err != nil {
		return repo.Backup{}, repo.State{}, err
	}
	b, s, err := repo.Restore(dir, n.Cfg.Host, id)
	if err != nil {
		return b, s, err
	}
	return b, s, repo.ClearBases(dir)
}

func (n *Node) Backups(name string) ([]repo.Backup, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	dir, err := n.Dir(name)
	if err != nil {
		return nil, err
	}
	return repo.Backups(dir)
}
