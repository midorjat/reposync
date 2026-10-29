// Package auto is the brain of the tray app: it watches for the peer,
// syncs on its own when that is safe, and reports what needs the user.
//
// It syncs when the peer becomes reachable (e.g. the laptop got home), when
// this machine wakes from sleep, every Interval while the peer stays
// reachable, and when asked (SyncNow, Resolve). Only automatic decisions are
// carried out; conflicts and first syncs are reported and wait for Resolve.
package auto

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"reposync/internal/node"
	"reposync/internal/repo"
	"reposync/internal/syncer"
)

type Notifier interface {
	Notify(title, body string)
}

// Status is what the tray shows.
type Status struct {
	PeerHost    string
	Reachable   bool
	Syncing     bool
	Paused      bool
	LastSync    time.Time
	NeedsChoice []string          // repos waiting for Resolve
	Errors      map[string]string // repo -> last error
}

type request struct {
	reason string
	repo   string // "" for all repos
	take   syncer.Side
}

type Agent struct {
	Node     *node.Node
	Dial     func(ctx context.Context) (*syncer.Syncer, error)
	Interval time.Duration // periodic sync while the peer is reachable
	Probe    time.Duration // how often to look for the peer
	Notify   Notifier      // may be nil
	OnStatus func(Status)  // may be nil; called from the agent goroutine
	Log      *log.Logger

	requests chan request

	mu       sync.Mutex
	status   Status
	notified map[string]string // repo -> what we last told the user about it
}

func (a *Agent) init() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.requests == nil {
		a.requests = make(chan request, 8)
		a.notified = map[string]string{}
		a.status.Errors = map[string]string{}
	}
}

// SyncNow asks for a sync of all repos as soon as possible.
func (a *Agent) SyncNow() { a.enqueue(request{reason: "requested"}) }

// Resolve syncs one repo that needs a decision, letting `take` win.
func (a *Agent) Resolve(name string, take syncer.Side) {
	a.enqueue(request{reason: "resolve", repo: name, take: take})
}

func (a *Agent) enqueue(r request) {
	a.init()
	select {
	case a.requests <- r:
	default: // queue full: a sync is coming anyway
	}
}

func (a *Agent) SetPaused(p bool) {
	a.update(func(s *Status) { s.Paused = p })
	if !p {
		a.SyncNow()
	}
}

func (a *Agent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.status
	s.NeedsChoice = append([]string(nil), s.NeedsChoice...)
	s.Errors = map[string]string{}
	for k, v := range a.status.Errors {
		s.Errors[k] = v
	}
	return s
}

func (a *Agent) update(f func(*Status)) {
	a.init()
	a.mu.Lock()
	f(&a.status)
	a.mu.Unlock()
	if a.OnStatus != nil {
		a.OnStatus(a.Status())
	}
}

// Run blocks until ctx is done.
func (a *Agent) Run(ctx context.Context) {
	a.init()
	probe := time.NewTicker(a.Probe)
	defer probe.Stop()
	lastTick := time.Now().Round(0) // wall clock: it keeps running while asleep
	var nextPeriodic time.Time
	wasReachable := false

	check := func(reason string, req *request) {
		s, err := a.dial(ctx)
		reachable := err == nil
		a.update(func(st *Status) {
			st.Reachable = reachable
			if s != nil {
				st.PeerHost = s.PeerHost
			}
		})
		if !reachable {
			if wasReachable {
				a.Log.Printf("peer unreachable: %v", err)
			}
			wasReachable = false
			if req != nil && req.reason != "" {
				a.notify("reposync", "Can't reach the other machine: "+shortErr(err))
			}
			return
		}
		if reason == "" {
			switch now := time.Now().Round(0); {
			case !wasReachable:
				reason = "peer appeared"
			case now.After(nextPeriodic):
				reason = "periodic"
			default:
				return
			}
		}
		wasReachable = true
		if a.Status().Paused && req == nil {
			return // paused stops automatic syncs; SyncNow and Resolve still run
		}
		if req == nil {
			req = &request{}
		}
		a.syncAll(ctx, s, reason, *req)
		nextPeriodic = time.Now().Round(0).Add(jitter(a.Interval))
	}

	check("", nil)
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-a.requests:
			check(req.reason, &req)
		case <-probe.C:
			now := time.Now().Round(0)
			woke := now.Sub(lastTick) > 3*a.Probe+time.Minute
			lastTick = now
			if woke {
				check("woke from sleep", nil)
			} else {
				check("", nil)
			}
		}
	}
}

func (a *Agent) dial(ctx context.Context) (*syncer.Syncer, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return a.Dial(ctx)
}

func (a *Agent) syncAll(ctx context.Context, s *syncer.Syncer, reason string, req request) {
	names := []string{req.repo}
	if req.repo == "" {
		names = names[:0]
		for _, r := range a.Node.Cfg.Repos {
			names = append(names, r.Name)
		}
	}
	a.Log.Printf("sync (%s): %s", reason, strings.Join(names, ", "))
	a.update(func(st *Status) { st.Syncing = true })
	defer a.update(func(st *Status) { st.Syncing = false; st.LastSync = time.Now() })

	for _, name := range names {
		p, msg, err := s.Sync(ctx, name, req.take)
		switch {
		case err != nil && transient(err):
			a.Log.Printf("%s: skipped this round: %v", name, err)
		case err != nil:
			a.Log.Printf("%s: error: %v", name, err)
			a.setRepo(name, false, err.Error())
			a.notifyOnce(name, "err:"+err.Error(), "reposync: "+name+" failed", shortErr(err))
		case (p.Action == syncer.Conflict || p.Action == syncer.Choose) && req.take == syncer.None:
			a.Log.Printf("%s: needs a decision: %s", name, p.Reason)
			a.setRepo(name, true, "")
			key := "choice:" + p.Local.Fingerprint() + p.Peer.Fingerprint()
			a.notifyOnce(name, key, name+" needs a decision",
				fmt.Sprintf("%s. Open the reposync tray menu to pick which copy wins.", capitalize(p.Reason)))
		default:
			a.Log.Printf("%s: %s", name, msg)
			a.setRepo(name, false, "")
			a.mu.Lock()
			delete(a.notified, name)
			a.mu.Unlock()
		}
	}
}

func (a *Agent) setRepo(name string, needsChoice bool, errText string) {
	a.update(func(st *Status) {
		list := st.NeedsChoice[:0:0]
		for _, n := range st.NeedsChoice {
			if n != name {
				list = append(list, n)
			}
		}
		if needsChoice {
			list = append(list, name)
			sort.Strings(list)
		}
		st.NeedsChoice = list
		if errText == "" {
			delete(st.Errors, name)
		} else {
			st.Errors[name] = errText
		}
	})
}

func (a *Agent) notifyOnce(name, key, title, body string) {
	a.mu.Lock()
	seen := a.notified[name] == key
	a.notified[name] = key
	a.mu.Unlock()
	if !seen {
		a.notify(title, body)
	}
}

func (a *Agent) notify(title, body string) {
	if a.Notify != nil {
		a.Notify.Notify(title, body)
	}
}

// transient errors clear up by themselves; retry next round without nagging.
func transient(err error) bool {
	return errors.Is(err, repo.ErrLocked) || errors.Is(err, repo.ErrChanged) || errors.Is(err, repo.ErrBusy)
}

func jitter(d time.Duration) time.Duration {
	// ±20%, so two agents on the same schedule drift apart.
	return d + time.Duration((rand.Float64()*0.4-0.2)*float64(d))
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
