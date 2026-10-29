package repo

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Base records what both sides looked like after the last successful sync
// with a peer. Comparing each side to it tells which side has new work.
type Base struct {
	Local string    `json:"local"` // fingerprint of this machine's state
	Peer  string    `json:"peer"`  // fingerprint of the peer's state
	Time  time.Time `json:"time"`
}

func basePath(dir, peer string) (string, error) {
	dd, err := Git{Dir: dir}.dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dd, "base-"+peer+".json"), nil
}

// LoadBase returns nil if the repo was never synced with peer.
func LoadBase(dir, peer string) (*Base, error) {
	p, err := basePath(dir, peer)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var b Base
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func SaveBase(dir, peer string, b Base) error {
	p, err := basePath(dir, peer)
	if err != nil {
		return err
	}
	if b.Time.IsZero() {
		b.Time = time.Now().UTC()
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ClearBases forgets every sync record, so the next sync asks which side wins.
func ClearBases(dir string) error {
	dd, err := Git{Dir: dir}.dataDir()
	if err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(dd, "base-*.json"))
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	return nil
}
