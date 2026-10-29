package auto_test

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"reposync/internal/auto"
	"reposync/internal/repo"
	"reposync/internal/syncer"
	. "reposync/internal/testenv"
)

type notes struct {
	mu   sync.Mutex
	list []string
}

func (n *notes) Notify(title, body string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.list = append(n.list, title+": "+body)
}

func (n *notes) count(prefix string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, s := range n.list {
		if strings.HasPrefix(s, prefix) {
			c++
		}
	}
	return c
}

func startAgent(t *testing.T, m *Machine, dial func(context.Context) (*syncer.Syncer, error), interval time.Duration, n *notes) *auto.Agent {
	m.Node.OnApplied = func(name, from string, _ repo.State) {
		(&auto.Arrivals{Notify: n, Delay: 0}).Add(from, name)
	}
	a := &auto.Agent{Node: m.Node, Dial: dial, Interval: interval, Probe: 20 * time.Millisecond,
		Notify: n, Log: log.New(io.Discard, "", 0)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return a
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPullsWhenPeerAppears(t *testing.T) {
	origin := NewOrigin(t)
	desktop := NewMachine(t, "desktop", origin)
	laptop := NewMachine(t, "laptop", origin)
	laptop.SyncWith(desktop, syncer.None)

	var online atomic.Bool
	dial := func(ctx context.Context) (*syncer.Syncer, error) {
		if !online.Load() {
			return nil, errors.New("no route to host")
		}
		return laptop.Dial(desktop)(ctx)
	}
	n := &notes{}
	a := startAgent(t, laptop, dial, time.Hour, n)

	desktop.Write("README.md", "written at home\n")
	time.Sleep(100 * time.Millisecond)
	Expect(t, "nothing happens while offline", laptop.Read("README.md"), "hello\n")

	online.Store(true) // the laptop got home
	eventually(t, "laptop pulls", func() bool { return laptop.Read("README.md") == "written at home\n" })
	eventually(t, "arrival notification", func() bool { return n.count("Work from desktop arrived") == 1 })
	eventually(t, "status reachable", func() bool { s := a.Status(); return s.Reachable && s.PeerHost == "desktop" })
}

func TestConflictWaitsForResolve(t *testing.T) {
	origin := NewOrigin(t)
	desktop := NewMachine(t, "desktop", origin)
	laptop := NewMachine(t, "laptop", origin)
	laptop.SyncWith(desktop, syncer.None)
	desktop.Write("README.md", "desktop\n")
	laptop.Write("README.md", "laptop\n")

	n := &notes{}
	a := startAgent(t, laptop, laptop.Dial(desktop), time.Hour, n)
	eventually(t, "needs choice", func() bool { s := a.Status(); return len(s.NeedsChoice) == 1 && s.NeedsChoice[0] == "app" })
	for i := 0; i < 3; i++ {
		a.SyncNow()
	}
	time.Sleep(300 * time.Millisecond)
	Expect(t, "notified once", n.count("app needs a decision"), 1)
	Expect(t, "laptop untouched", laptop.Read("README.md"), "laptop\n")
	Expect(t, "desktop untouched", desktop.Read("README.md"), "desktop\n")

	a.Resolve("app", syncer.PeerSide)
	eventually(t, "resolved", func() bool { return laptop.Read("README.md") == "desktop\n" && len(a.Status().NeedsChoice) == 0 })
	if !strings.HasPrefix(laptop.Git("branch", "--list", "sync/*", "--format=%(refname:short)"), "sync/laptop/main-") {
		t.Fatal("laptop's work should be kept on a sync/laptop/* branch")
	}
}

// Both machines run agents that sync with each other at the same time; the
// cross-machine lock must keep them from interfering.
func TestBothAgentsAtOnce(t *testing.T) {
	origin := NewOrigin(t)
	desktop := NewMachine(t, "desktop", origin)
	laptop := NewMachine(t, "laptop", origin)
	laptop.SyncWith(desktop, syncer.None)

	da := startAgent(t, desktop, desktop.Dial(laptop), 30*time.Millisecond, &notes{})
	la := startAgent(t, laptop, laptop.Dial(desktop), 30*time.Millisecond, &notes{})

	desktop.Write("a.txt", "from desktop\n")
	eventually(t, "laptop gets a.txt", func() bool { return laptop.Read("a.txt") == "from desktop\n" })
	time.Sleep(200 * time.Millisecond)
	laptop.Write("b.txt", "from laptop\n")
	eventually(t, "desktop gets b.txt", func() bool { return desktop.Read("b.txt") == "from laptop\n" })

	for _, a := range []*auto.Agent{da, la} {
		s := a.Status()
		if len(s.Errors) > 0 || len(s.NeedsChoice) > 0 {
			t.Fatalf("unexpected trouble: %+v", s)
		}
	}
}
