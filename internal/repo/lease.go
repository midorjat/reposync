package repo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrLocked = errors.New("another sync of this repo is in progress")

// A lease is a lock file in .git/reposync that one sync holds on both
// machines for its whole run, so two syncs (the tray agents on both sides,
// or the CLI next to a tray agent) never interleave. It expires on its own
// if the holder dies.

func leasePath(dir string) (string, error) {
	dd, err := Git{Dir: dir}.dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dd, "sync.lease"), nil
}

// AcquireLease takes the lease for holder, or refreshes it if holder already has it.
func AcquireLease(dir, holder string, ttl time.Duration) error {
	p, err := leasePath(dir)
	if err != nil {
		return err
	}
	content := holder + "\n" + time.Now().Add(ttl).UTC().Format(time.RFC3339Nano) + "\n"
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := f.WriteString(content)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			return werr
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		cur, expires, rerr := readLease(p)
		if rerr == nil && time.Now().Before(expires) {
			if cur == holder {
				return os.WriteFile(p, []byte(content), 0o644)
			}
			return fmt.Errorf("%w (%s)", ErrLocked, cur)
		}
		os.Remove(p) // expired or unreadable: take it over
	}
	return ErrLocked
}

// ReleaseLease drops the lease if holder has it.
func ReleaseLease(dir, holder string) error {
	p, err := leasePath(dir)
	if err != nil {
		return err
	}
	cur, _, err := readLease(p)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && cur != holder) {
		return nil
	}
	return os.Remove(p)
}

// LeaseHolder reports who holds an unexpired lease, or "".
func LeaseHolder(dir string) (string, error) {
	p, err := leasePath(dir)
	if err != nil {
		return "", err
	}
	cur, expires, err := readLease(p)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && time.Now().After(expires)) {
		return "", nil
	}
	return cur, err
}

func readLease(p string) (string, time.Time, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return "", time.Time{}, err
	}
	holder, exp, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	t, err := time.Parse(time.RFC3339Nano, exp)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("bad lease file %s", p)
	}
	return holder, t, nil
}
