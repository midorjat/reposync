package repo

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const keepBackups = 20

// Apply makes the repo match target: same branches, same checked-out branch,
// same files. The current state is backed up first and can be restored with
// Restore. If expect is set and the repo no longer has that fingerprint,
// nothing is touched and ErrChanged is returned.
//
// Everything comes back as unstaged changes; what was staged is not kept.
func Apply(dir, host string, target State, expect string) (State, error) {
	g := Git{Dir: dir}
	cur, err := Capture(dir, host)
	if err != nil {
		return State{}, err
	}
	if expect != "" && cur.Fingerprint() != expect {
		return State{}, ErrChanged
	}
	id, err := backup(g, cur)
	if err != nil {
		return State{}, fmt.Errorf("backing up before applying: %w", err)
	}
	if err := checkout(g, cur, target); err != nil {
		return State{}, fmt.Errorf("%w\n(the previous state is saved; restore it with `reposync undo --to %s`)", err, id)
	}
	return Capture(dir, host)
}

func checkout(g Git, cur, target State) (err error) {
	// Load cur's snapshot as the index so read-tree knows about every file
	// on disk, including untracked ones; files the target doesn't have are
	// then removed instead of left behind.
	if _, err := g.Run("read-tree", cur.WipTree); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			g.Run("reset", "-q") // put the index back on HEAD
		}
	}()
	g.Run("update-index", "-q", "--refresh")
	if _, err := g.Run("read-tree", "-m", "-u", cur.WipTree, target.WipTree); err != nil {
		return err
	}

	// Deletes first, so e.g. "feat/x" can go away before "feat" is created.
	dels, ups := map[string]*string{}, map[string]*string{}
	for name := range cur.Branches {
		if _, ok := target.Branches[name]; !ok {
			dels["refs/heads/"+name] = nil
		}
	}
	for name, sha := range target.Branches {
		if cur.Branches[name] != sha {
			ups["refs/heads/"+name] = &sha
		}
	}
	if err := g.updateRefs(dels); err != nil {
		return err
	}
	if err := g.updateRefs(ups); err != nil {
		return err
	}
	if target.Branch != "" {
		_, err = g.Run("symbolic-ref", "HEAD", "refs/heads/"+target.Branch)
	} else {
		_, err = g.Run("update-ref", "--no-deref", "HEAD", target.Head)
	}
	if err != nil {
		return err
	}
	// Index back to the new HEAD; the target's work shows up as unstaged changes.
	_, err = g.Run("reset", "-q")
	return err
}

func backup(g Git, s State) (string, error) {
	commit, err := writeStateCommit(g, s)
	if err != nil {
		return "", err
	}
	s.Commit = commit
	id := time.Now().UTC().Format("20060102T150405.000000Z")
	if err := writeSnapshotRefs(g, backupPrefix+id+"/", s); err != nil {
		return "", err
	}
	return id, pruneBackups(g)
}

type Backup struct {
	ID    string
	State State
}

// Backups lists saved states, newest first.
func Backups(dir string) ([]Backup, error) {
	g := Git{Dir: dir}
	refs, err := g.listRefs(backupPrefix)
	if err != nil {
		return nil, err
	}
	var out []Backup
	for ref := range refs {
		id, ok := strings.CutSuffix(strings.TrimPrefix(ref, backupPrefix), "/state")
		if !ok || strings.Contains(id, "/") {
			continue
		}
		s, err := readStateCommit(g, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, Backup{ID: id, State: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func pruneBackups(g Git) error {
	all, err := Backups(g.Dir)
	if err != nil {
		return err
	}
	for i := keepBackups; i < len(all); i++ {
		if err := g.replaceRefs(backupPrefix+all[i].ID+"/", nil); err != nil {
			return err
		}
	}
	return nil
}

// Restore applies a backup (the newest if id is empty). Restoring makes a
// backup too, so it can itself be undone.
func Restore(dir, host, id string) (Backup, State, error) {
	all, err := Backups(dir)
	if err != nil {
		return Backup{}, State{}, err
	}
	for _, b := range all {
		if id == "" || b.ID == id {
			s, err := Apply(dir, host, b.State, "")
			return b, s, err
		}
	}
	if id == "" {
		return Backup{}, State{}, fmt.Errorf("no backups yet")
	}
	return Backup{}, State{}, fmt.Errorf("no backup %q (see `reposync backups`)", id)
}

// KeepWork saves the work last received from host `from` on a local branch
// sync/<from>/<branch>-<time>, unless this repo already has it: it is clean
// and its HEAD is on one of our branches. Returns the branch name, or "".
func KeepWork(dir, from string) (string, error) {
	g := Git{Dir: dir}
	s, err := Imported(dir, from)
	if err != nil {
		return "", err
	}
	if !s.Dirty() {
		contained, err := g.Run("for-each-ref", "--count=1", "--contains", s.Head, "refs/heads/")
		if err != nil {
			return "", err
		}
		if contained != "" {
			return "", nil
		}
	}
	label := s.Branch
	if label == "" {
		label = "detached"
	}
	name := fmt.Sprintf("sync/%s/%s-%s", from, label, time.Now().Format("20060102-150405"))
	if _, err := g.Run("branch", name, s.Commit); err != nil {
		return "", err
	}
	return name, nil
}
