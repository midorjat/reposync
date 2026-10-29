// Command reposync-tray runs reposync in the background: it serves the other
// machine, syncs automatically when that is safe, shows a status icon and
// notifies you when work arrives or a decision is needed.
//
// On Windows it is built with -H windowsgui so no console window appears.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"fyne.io/systray"

	"reposync/internal/agent"
	"reposync/internal/auto"
	"reposync/internal/config"
	"reposync/internal/desktop"
	"reposync/internal/node"
	"reposync/internal/repo"
	"reposync/internal/syncer"
)

type quiet struct{}

func (quiet) Notify(string, string) {}

func main() {
	cfgPath := flag.String("config", "", "config file")
	flag.Parse()
	logger := openLog()
	toast := desktop.Notifier{Log: logger}

	if *cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			fatal(logger, toast, err)
		}
		*cfgPath = p
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal(logger, toast, err)
	}
	peerURL, err := cfg.PeerURL()
	if err != nil {
		fatal(logger, toast, err)
	}

	var notifier auto.Notifier = toast
	if !cfg.Auto.Notifying() {
		notifier = quiet{}
	}
	n := node.New(cfg)
	arrivals := &auto.Arrivals{Notify: notifier, Delay: 3 * time.Second}
	n.OnApplied = func(name, from string, s repo.State) {
		logger.Printf("%s: now matches %s (%s)", name, from, s.Label())
		arrivals.Add(from, name)
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fatal(logger, toast, fmt.Errorf("can't listen on %s; is reposync already running? (%v)", cfg.Listen, err))
	}
	srv := &http.Server{Handler: agent.NewServer(n, cfg.Token, logger)}
	go srv.Serve(ln)

	client := agent.NewClient(peerURL, cfg.Token)
	a := &auto.Agent{
		Node:     n,
		Dial:     func(ctx context.Context) (*syncer.Syncer, error) { return syncer.New(ctx, n, client) },
		Interval: cfg.Auto.Every(),
		Probe:    15 * time.Second,
		Notify:   notifier,
		Log:      logger,
	}
	logger.Printf("reposync-tray %s: %q serving %d repos on %s, peer %s, auto=%v every %s",
		node.Version, cfg.Host, len(cfg.Repos), cfg.Listen, peerURL, cfg.Auto.On(), a.Interval)

	t := &tray{cfg: cfg, cfgPath: *cfgPath, agent: a, log: logger}
	ctx, cancel := context.WithCancel(context.Background())
	systray.Run(func() { t.ready(ctx) }, func() {
		cancel()
		srv.Close()
		logger.Printf("stopped")
	})
}

func openLog() *log.Logger {
	p, err := desktop.LogPath()
	if err != nil {
		return log.New(io.Discard, "", 0)
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	if st, err := os.Stat(p); err == nil && st.Size() > 5<<20 {
		os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return log.New(io.Discard, "", 0)
	}
	return log.New(f, "", log.LstdFlags)
}

func fatal(logger *log.Logger, toast desktop.Notifier, err error) {
	logger.Printf("fatal: %v", err)
	toast.Notify("reposync can't start", err.Error())
	os.Exit(1)
}

type tray struct {
	cfg     *config.Config
	cfgPath string
	agent   *auto.Agent
	log     *log.Logger

	status  *systray.MenuItem
	decide  *systray.MenuItem
	choices map[string][2]*systray.MenuItem // repo -> keep this machine's, keep the peer's
	paused  *systray.MenuItem
}

func (t *tray) ready(ctx context.Context) {
	systray.SetIcon(makeIcon(grey))
	systray.SetTitle("reposync")
	systray.SetTooltip("reposync")

	t.status = systray.AddMenuItem("Starting…", "")
	t.status.Disable()
	syncNow := systray.AddMenuItem("Sync now", "Sync all repos with the other machine")
	t.decide = systray.AddMenuItem("Needs a decision", "Repos changed on both machines")
	t.choices = map[string][2]*systray.MenuItem{}
	for _, r := range t.cfg.Repos {
		mine := t.decide.AddSubMenuItem(r.Name+": keep "+t.cfg.Host+"'s copy", "")
		theirs := t.decide.AddSubMenuItem(r.Name+": keep the other machine's copy", "")
		mine.Hide()
		theirs.Hide()
		t.choices[r.Name] = [2]*systray.MenuItem{mine, theirs}
		go t.onClick(ctx, mine, func() { t.agent.Resolve(r.Name, syncer.LocalSide) })
		go t.onClick(ctx, theirs, func() { t.agent.Resolve(r.Name, syncer.PeerSide) })
	}
	t.decide.Hide()
	systray.AddSeparator()
	t.paused = systray.AddMenuItemCheckbox("Pause automatic sync", "", !t.cfg.Auto.On())
	showStatus := systray.AddMenuItem("Show status…", "Open a window with `reposync status`")
	openCfg := systray.AddMenuItem("Open config", t.cfgPath)
	openLog := systray.AddMenuItem("Open log", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Stop syncing and serving the other machine")

	go t.onClick(ctx, syncNow, t.agent.SyncNow)
	go t.onClick(ctx, t.paused, func() {
		if t.paused.Checked() {
			t.paused.Uncheck()
		} else {
			t.paused.Check()
		}
		t.agent.SetPaused(t.paused.Checked())
	})
	go t.onClick(ctx, showStatus, func() { t.try(desktop.OpenStatus()) })
	go t.onClick(ctx, openCfg, func() { t.try(desktop.OpenFile(t.cfgPath)) })
	go t.onClick(ctx, openLog, func() {
		if p, err := desktop.LogPath(); err == nil {
			t.try(desktop.OpenFile(p))
		}
	})
	go t.onClick(ctx, quit, systray.Quit)

	t.agent.OnStatus = t.show
	t.agent.SetPaused(!t.cfg.Auto.On())
	go t.agent.Run(ctx)
}

func (t *tray) onClick(ctx context.Context, item *systray.MenuItem, f func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-item.ClickedCh:
			f()
		}
	}
}

func (t *tray) try(err error) {
	if err != nil {
		t.log.Printf("tray action failed: %v", err)
	}
}

func (t *tray) show(s auto.Status) {
	peer := s.PeerHost
	if peer == "" {
		peer = t.cfg.Peer
	}
	var text string
	icon := green
	switch {
	case s.Syncing:
		text, icon = "Syncing with "+peer+"…", blue
	case len(s.NeedsChoice) > 0:
		text, icon = fmt.Sprintf("%d repo(s) need a decision", len(s.NeedsChoice)), orange
	case len(s.Errors) > 0:
		text, icon = fmt.Sprintf("%d repo(s) failed, see log", len(s.Errors)), orange
	case !s.Reachable:
		text, icon = peer+" not reachable", grey
	case s.Paused:
		text, icon = "Automatic sync paused", grey
	case s.LastSync.IsZero():
		text = "Connected to " + peer
	default:
		text = "In sync with " + peer + " · " + s.LastSync.Format("15:04")
	}
	t.status.SetTitle(text)
	systray.SetTooltip("reposync: " + text)
	systray.SetIcon(makeIcon(icon))

	needs := map[string]bool{}
	for _, name := range s.NeedsChoice {
		needs[name] = true
	}
	for name, items := range t.choices {
		items[1].SetTitle(name + ": keep " + peer + "'s copy")
		for _, it := range items {
			if needs[name] {
				it.Show()
			} else {
				it.Hide()
			}
		}
	}
	if len(needs) > 0 {
		t.decide.Show()
	} else {
		t.decide.Hide()
	}
}
