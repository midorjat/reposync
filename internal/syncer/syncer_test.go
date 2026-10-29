package syncer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"reposync/internal/syncer"
	. "reposync/internal/testenv"
)

func TestHandoffBothWays(t *testing.T) {
	origin := NewOrigin(t)
	desktop := NewMachine(t, "desktop", origin)
	laptop := NewMachine(t, "laptop", origin)

	// Fresh clones are identical: in sync, and that becomes the base.
	act, _ := laptop.SyncWith(desktop, syncer.None)
	Expect(t, "fresh clones", act, syncer.InSync)

	// Work on the desktop: modify, add untracked, delete, and an ignored file.
	desktop.Write("README.md", "hello from desktop\n")
	desktop.Write("src/new.go", "package src\n")
	os.Remove(filepath.Join(desktop.Dir, "old.txt"))
	desktop.Write("debug.log", "ignored\n")
	desktop.Git("add", "README.md") // staged, to check the real index is left alone by snapshots

	// Morning: the laptop pulls it all.
	act, msg := laptop.SyncWith(desktop, syncer.None)
	Expect(t, "laptop pulls ("+msg+")", act, syncer.Pull)
	Expect(t, "README on laptop", laptop.Read("README.md"), "hello from desktop\n")
	Expect(t, "untracked file on laptop", laptop.Read("src/new.go"), "package src\n")
	Expect(t, "deleted file on laptop", laptop.Read("old.txt"), "<missing>")
	Expect(t, "ignored file not synced", laptop.Read("debug.log"), "<missing>")
	Expect(t, "desktop index untouched", desktop.Git("diff", "--cached", "--name-only"), "README.md")

	// Nothing changed since: in sync both ways.
	act, _ = laptop.SyncWith(desktop, syncer.None)
	Expect(t, "after pull", act, syncer.InSync)
	act, _ = desktop.SyncWith(laptop, syncer.None)
	Expect(t, "after pull, from desktop", act, syncer.InSync)

	// At the office: commit on a new branch, keep some WIP on top.
	laptop.Git("checkout", "-q", "-b", "feature")
	laptop.Git("add", "-A")
	laptop.Git("commit", "-q", "-m", "work from desktop")
	laptop.Write("src/new.go", "package src\n\nfunc Office() {}\n")
	head := laptop.Git("rev-parse", "HEAD")

	// Evening: the laptop pushes; the desktop continues exactly there.
	act, msg = laptop.SyncWith(desktop, syncer.None)
	Expect(t, "laptop pushes ("+msg+")", act, syncer.Push)
	Expect(t, "desktop branch", desktop.Git("branch", "--show-current"), "feature")
	Expect(t, "desktop HEAD", desktop.Git("rev-parse", "HEAD"), head)
	Expect(t, "desktop WIP", desktop.Read("src/new.go"), "package src\n\nfunc Office() {}\n")
	Expect(t, "desktop status", desktop.Git("status", "--porcelain"), "M src/new.go")
	Expect(t, "desktop ignored file left alone", desktop.Read("debug.log"), "ignored\n")

	// The desktop can start the next sync too: it edits, then pushes.
	desktop.Write("README.md", "desktop again\n")
	act, _ = desktop.SyncWith(laptop, syncer.None)
	Expect(t, "desktop pushes", act, syncer.Push)
	Expect(t, "laptop README", laptop.Read("README.md"), "desktop again\n")
}

func TestConflictNeedsChoiceAndKeepsWork(t *testing.T) {
	origin := NewOrigin(t)
	desktop := NewMachine(t, "desktop", origin)
	laptop := NewMachine(t, "laptop", origin)
	laptop.SyncWith(desktop, syncer.None)

	desktop.Write("README.md", "desktop version\n")
	laptop.Write("README.md", "laptop version\n")

	act, msg := laptop.SyncWith(desktop, syncer.None)
	Expect(t, "plan", act, syncer.Conflict)
	if !strings.Contains(msg, "skipped") {
		t.Fatalf("conflict without --take should be skipped, got %q", msg)
	}
	Expect(t, "desktop untouched", desktop.Read("README.md"), "desktop version\n")
	Expect(t, "laptop untouched", laptop.Read("README.md"), "laptop version\n")

	// Laptop wins; the desktop's edit survives on a branch on both machines.
	act, msg = laptop.SyncWith(desktop, syncer.LocalSide)
	Expect(t, "take laptop ("+msg+")", act, syncer.Conflict)
	Expect(t, "desktop now has laptop's file", desktop.Read("README.md"), "laptop version\n")
	kept := desktop.Git("for-each-ref", "--format=%(refname:short)", "refs/heads/sync/")
	if !strings.HasPrefix(kept, "sync/desktop/main-") {
		t.Fatalf("expected a sync/desktop/main-* branch, got %q", kept)
	}
	Expect(t, "kept work content", desktop.Git("show", kept+":README.md"), "desktop version")

	act, _ = desktop.SyncWith(laptop, syncer.None)
	Expect(t, "settled", act, syncer.InSync)

	// Undo on the desktop brings back what it had before.
	b, _, err := desktop.Node.Undo("app", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID == "" {
		t.Fatal("no backup id")
	}
	Expect(t, "undo restores", desktop.Read("README.md"), "desktop version\n")
	act, _ = laptop.SyncWith(desktop, syncer.None)
	Expect(t, "after undo the next sync asks", act, syncer.Choose)
}

func TestFirstSyncWithDifferencesAsks(t *testing.T) {
	origin := NewOrigin(t)
	desktop := NewMachine(t, "desktop", origin)
	laptop := NewMachine(t, "laptop", origin)
	desktop.Write("wip.txt", "unsaved\n")

	act, _ := laptop.SyncWith(desktop, syncer.None)
	Expect(t, "first sync", act, syncer.Choose)
	Expect(t, "laptop untouched", laptop.Read("wip.txt"), "<missing>")

	act, _ = laptop.SyncWith(desktop, syncer.PeerSide)
	Expect(t, "take desktop", act, syncer.Choose)
	Expect(t, "laptop got it", laptop.Read("wip.txt"), "unsaved\n")
	// The laptop was a clean clone already on main: nothing to keep.
	Expect(t, "no kept branch", laptop.Git("branch", "--list", "sync/*"), "")
}
