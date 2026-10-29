// Package syncer decides, per repo, which machine has the newer work and
// moves it to the other one.
//
// Each side's current state is compared with the base: what both sides
// looked like after the last successful sync. If only one side changed, it
// wins automatically. If both changed (or they never synced), nothing happens
// until the user picks a side with --take; the losing side's work is then
// kept on a sync/<host>/... branch, and it is backed up on its own machine.
package syncer

import (
	"context"
	"fmt"
	"io"
	"os"

	"reposync/internal/node"
	"reposync/internal/repo"
)

type Action string

const (
	InSync   Action = "in sync"
	Pull     Action = "pull"
	Push     Action = "push"
	Conflict Action = "conflict"
	Choose   Action = "choose"
	Failed   Action = "error"
)

// Side names which machine wins a Choose or Conflict.
type Side string

const (
	None      Side = ""
	LocalSide Side = "local"
	PeerSide  Side = "peer"
)

type Plan struct {
	Repo   string
	Local  repo.State
	Peer   repo.State
	Action Action
	Reason string
	Take   Side
	Err    error

	base *repo.Base
}

type Syncer struct {
	Local     *node.Node
	Peer      node.Peer
	LocalHost string
	PeerHost  string
}

func New(ctx context.Context, local *node.Node, peer node.Peer) (*Syncer, error) {
	h, err := peer.Hello(ctx)
	if err != nil {
		return nil, err
	}
	if h.Host == local.Cfg.Host {
		return nil, fmt.Errorf("the peer is also called %q; give each machine its own host name", h.Host)
	}
	return &Syncer{Local: local, Peer: peer, LocalHost: local.Cfg.Host, PeerHost: h.Host}, nil
}

// Plan works out what Run would do, without changing anything.
func (s *Syncer) Plan(ctx context.Context, name string, take Side) Plan {
	p := Plan{Repo: name}
	var err error
	if p.Local, err = s.Local.State(ctx, name); err != nil {
		return p.fail(fmt.Errorf("%s: %w", s.LocalHost, err))
	}
	if p.Peer, err = s.Peer.State(ctx, name); err != nil {
		return p.fail(fmt.Errorf("%s: %w", s.PeerHost, err))
	}
	if p.base, err = s.base(ctx, name); err != nil {
		return p.fail(err)
	}
	lf, pf := p.Local.Fingerprint(), p.Peer.Fingerprint()
	if lf == pf {
		p.Action = InSync
		return p
	}
	if p.base == nil {
		p.Action, p.Reason = Choose, "no shared sync record (first sync, or undo) and the copies differ"
	} else {
		localChanged, peerChanged := lf != p.base.Local, pf != p.base.Peer
		switch {
		case peerChanged && !localChanged:
			p.Action, p.Reason = Pull, s.PeerHost+" has newer work"
		case localChanged && !peerChanged:
			p.Action, p.Reason = Push, s.LocalHost+" has newer work"
		case localChanged && peerChanged:
			p.Action, p.Reason = Conflict, "both machines changed since the last sync"
		default:
			p.Action, p.Reason = Choose, "copies differ but neither changed since the last sync"
		}
	}
	if p.Action == Choose || p.Action == Conflict {
		p.Take = take
	}
	return p
}

// base returns the record of the last sync, if both machines agree on it.
// They disagree after an undo or a sync that failed halfway; then neither
// side can be trusted to be "unchanged" and the user has to choose.
func (s *Syncer) base(ctx context.Context, name string) (*repo.Base, error) {
	mine, err := s.Local.Base(ctx, name, s.PeerHost)
	if err != nil || mine == nil {
		return nil, err
	}
	theirs, err := s.Peer.Base(ctx, name, s.LocalHost)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.PeerHost, err)
	}
	if theirs == nil || theirs.Local != mine.Peer || theirs.Peer != mine.Local {
		return nil, nil
	}
	return mine, nil
}

func (p Plan) fail(err error) Plan {
	p.Action, p.Err = Failed, err
	return p
}

// Run carries out a plan and describes what it did.
func (s *Syncer) Run(ctx context.Context, p Plan) (string, error) {
	switch p.Action {
	case Failed:
		return "", p.Err
	case InSync:
		f := p.Local.Fingerprint()
		if p.base == nil || p.base.Local != f || p.base.Peer != f {
			if err := s.setBases(ctx, p.Repo, f, f); err != nil {
				return "", err
			}
		}
		return "already in sync", nil
	case Pull:
		return s.pull(ctx, p)
	case Push:
		return s.push(ctx, p)
	}

	// Choose or Conflict: only with an explicit side.
	switch p.Take {
	case LocalSide:
		if _, err := s.fetch(ctx, p.Repo); err != nil {
			return "", err
		}
		kept, err := s.Local.KeepWork(ctx, p.Repo, s.PeerHost)
		if err != nil {
			return "", err
		}
		msg, err := s.push(ctx, p)
		return keptNote(msg, kept, s.PeerHost), err
	case PeerSide:
		if _, err := s.send(ctx, p.Repo); err != nil {
			return "", err
		}
		kept, err := s.Peer.KeepWork(ctx, p.Repo, s.LocalHost)
		if err != nil {
			return "", err
		}
		msg, err := s.pull(ctx, p)
		return keptNote(msg, kept, s.LocalHost), err
	}
	return fmt.Sprintf("skipped: %s; run `reposync sync --take %s %s` or `--take %s` to choose which copy wins",
		p.Reason, s.LocalHost, p.Repo, s.PeerHost), nil
}

func keptNote(msg, branch, host string) string {
	if msg == "" || branch == "" {
		return msg
	}
	return fmt.Sprintf("%s; %s's work kept on branch %s", msg, host, branch)
}

func (s *Syncer) pull(ctx context.Context, p Plan) (string, error) {
	theirs, err := s.fetch(ctx, p.Repo)
	if err != nil {
		return "", err
	}
	mine, err := s.Local.Apply(ctx, p.Repo, s.PeerHost, p.Local.Fingerprint())
	if err != nil {
		return "", err
	}
	if err := s.setBases(ctx, p.Repo, mine.Fingerprint(), theirs.Fingerprint()); err != nil {
		return "", err
	}
	return fmt.Sprintf("pulled from %s, now %s", s.PeerHost, mine.Label()), nil
}

func (s *Syncer) push(ctx context.Context, p Plan) (string, error) {
	mine, err := s.send(ctx, p.Repo)
	if err != nil {
		return "", err
	}
	theirs, err := s.Peer.Apply(ctx, p.Repo, s.LocalHost, p.Peer.Fingerprint())
	if err != nil {
		return "", err
	}
	if err := s.setBases(ctx, p.Repo, mine.Fingerprint(), theirs.Fingerprint()); err != nil {
		return "", err
	}
	return fmt.Sprintf("pushed to %s, now %s", s.PeerHost, theirs.Label()), nil
}

// fetch brings the peer's current snapshot here (without applying it).
func (s *Syncer) fetch(ctx context.Context, name string) (repo.State, error) {
	return viaTempFile(
		func(w io.Writer) error { return s.Peer.Export(ctx, name, w) },
		func(r io.Reader) (repo.State, error) { return s.Local.Import(ctx, name, s.PeerHost, r) },
	)
}

// send takes a snapshot here and stores it on the peer (without applying it).
func (s *Syncer) send(ctx context.Context, name string) (repo.State, error) {
	return viaTempFile(
		func(w io.Writer) error { return s.Local.Export(ctx, name, w) },
		func(r io.Reader) (repo.State, error) { return s.Peer.Import(ctx, name, s.LocalHost, r) },
	)
}

func viaTempFile(write func(io.Writer) error, read func(io.Reader) (repo.State, error)) (repo.State, error) {
	f, err := os.CreateTemp("", "reposync-*.bundle")
	if err != nil {
		return repo.State{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := write(f); err != nil {
		return repo.State{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return repo.State{}, err
	}
	return read(f)
}

func (s *Syncer) setBases(ctx context.Context, name, local, peer string) error {
	if err := s.Local.SetBase(ctx, name, s.PeerHost, repo.Base{Local: local, Peer: peer}); err != nil {
		return err
	}
	return s.Peer.SetBase(ctx, name, s.LocalHost, repo.Base{Local: peer, Peer: local})
}
