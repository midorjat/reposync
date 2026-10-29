package repo

import (
	"io"
	"os"
)

// ExportBundle snapshots the repo and writes the snapshot plus all local
// branches into a git bundle file. The caller removes the file.
func ExportBundle(dir, host string) (State, string, error) {
	g := Git{Dir: dir}
	s, err := Capture(dir, host)
	if err != nil {
		return State{}, "", err
	}
	if s.Commit, err = writeStateCommit(g, s); err != nil {
		return State{}, "", err
	}
	if err := writeSnapshotRefs(g, selfPrefix, s); err != nil {
		return State{}, "", err
	}
	dd, err := g.dataDir()
	if err != nil {
		return State{}, "", err
	}
	path, err := tempFile(dd, "export-*.bundle")
	if err != nil {
		return State{}, "", err
	}
	// TODO: send only what the peer lacks (bundle prerequisites) instead of
	// full history every time.
	if _, err := g.Run("bundle", "create", path, "--glob="+selfPrefix+"*"); err != nil {
		os.Remove(path)
		return State{}, "", err
	}
	return s, path, nil
}

// Import stores a bundle received from host `from` under
// refs/reposync/peers/<from>/ and returns the state it carries.
// The working copy is not touched.
func Import(dir, from string, r io.Reader) (State, error) {
	g := Git{Dir: dir}
	dd, err := g.dataDir()
	if err != nil {
		return State{}, err
	}
	path, err := tempFile(dd, "import-*.bundle")
	if err != nil {
		return State{}, err
	}
	defer os.Remove(path)
	f, err := os.Create(path)
	if err != nil {
		return State{}, err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return State{}, err
	}
	if err := f.Close(); err != nil {
		return State{}, err
	}

	prefix := peersPrefix + from + "/"
	if err := g.replaceRefs(prefix, nil); err != nil { // drop branches the peer no longer has
		return State{}, err
	}
	if _, err := g.Run("fetch", "--quiet", "--no-tags", "--no-write-fetch-head", path, "+"+selfPrefix+"*:"+prefix+"*"); err != nil {
		return State{}, err
	}
	return Imported(dir, from)
}

// Imported returns the last state received from host `from`.
func Imported(dir, from string) (State, error) {
	return readStateCommit(Git{Dir: dir}, peersPrefix+from+"/state")
}
