// Command reposync keeps the same set of git repos in the same state on two
// machines: branches, checked-out branch and uncommitted work.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"reposync/internal/agent"
	"reposync/internal/config"
	"reposync/internal/desktop"
	"reposync/internal/doctor"
	"reposync/internal/node"
	"reposync/internal/syncer"
)

const usage = `reposync keeps your repos identical on two machines, uncommitted work included.

Usage:
  reposync [-config FILE] <command> [flags] [repo...]

Commands:
  init       write this machine's config (once per machine)
  serve      run the agent so the other machine can sync with this one
  status     show what sync would do, without changing anything
  sync       bring both machines up to date (--take HOST to settle conflicts)
  bootstrap  clone repos from the config that are missing on this machine
  backups    list saved states of a repo
  undo       restore the state from before the last sync (or --to ID)
  doctor     check that both machines are set up the same way
  autostart  on|off: start the tray app (reposync-tray) when you log in
  version    print the version

Run "reposync <command> -h" for a command's flags.
`

func main() {
	log.SetFlags(0)
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	cfgPath := flag.String("config", "", "config file (default: "+defaultPathHint()+")")
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			log.Fatal(err)
		}
		*cfgPath = p
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd, args := flag.Arg(0), flag.Args()[1:]
	var err error
	switch cmd {
	case "init":
		err = cmdInit(*cfgPath, args)
	case "serve":
		err = cmdServe(ctx, *cfgPath, args)
	case "status":
		err = cmdStatus(ctx, *cfgPath, args)
	case "sync":
		err = cmdSync(ctx, *cfgPath, args)
	case "bootstrap":
		err = cmdBootstrap(*cfgPath, args)
	case "backups":
		err = cmdBackups(*cfgPath, args)
	case "undo":
		err = cmdUndo(*cfgPath, args)
	case "doctor":
		err = cmdDoctor(ctx, *cfgPath, args)
	case "autostart":
		err = cmdAutostart(args)
	case "version":
		fmt.Println("reposync", node.Version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal("error: ", err)
	}
}

func defaultPathHint() string {
	if p, err := config.DefaultPath(); err == nil {
		return p
	}
	return "user config dir"
}

func cmdInit(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	hostname, _ := os.Hostname()
	home, _ := os.UserHomeDir()
	host := fs.String("host", plainName(hostname), "name of this machine")
	peer := fs.String("peer", "", "the other machine, e.g. desktop or desktop:7420 (required)")
	root := fs.String("root", filepath.Join(home, "code"), "folder your repos live under")
	token := fs.String("token", "", "shared secret; leave empty on the first machine, copy it to the second")
	force := fs.Bool("force", false, "overwrite an existing config")
	fs.Parse(args)
	if *peer == "" {
		return errors.New("--peer is required (the other machine's name)")
	}
	if _, err := os.Stat(cfgPath); err == nil && !*force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", cfgPath)
	}
	newToken := *token == ""
	if newToken {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		*token = hex.EncodeToString(b)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, []byte(config.Template(*host, *peer, *root, *token)), 0o600); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n\n", cfgPath)
	fmt.Println("Next:")
	fmt.Println("  1. Add your repos as [[repo]] entries in that file.")
	if newToken {
		fmt.Printf("  2. On the other machine run:\n       reposync init --host %s --token %s\n     and copy the [[repo]] list over.\n", *peer, *token)
	} else {
		fmt.Println("  2. Make sure the [[repo]] list matches the other machine's.")
	}
	fmt.Println("  3. Run `reposync serve` on one machine and `reposync status` on the other.")
	return nil
}

func plainName(s string) string {
	s = strings.ToLower(strings.SplitN(s, ".", 2)[0])
	s = regexp.MustCompile(`[^a-z0-9_-]+`).ReplaceAllString(s, "-")
	if s == "" {
		return "machine"
	}
	return s
}

func load(cfgPath string) (*config.Config, *node.Node, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	return cfg, node.New(cfg), nil
}

func cmdServe(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.Parse(args)
	cfg, n, err := load(cfgPath)
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	srv := &http.Server{Addr: cfg.Listen, Handler: agent.NewServer(n, cfg.Token, logger)}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	logger.Printf("reposync %s serving %d repos as %q on %s (Ctrl+C to stop)", node.Version, len(cfg.Repos), cfg.Host, cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func connect(ctx context.Context, cfg *config.Config, n *node.Node) (*syncer.Syncer, error) {
	u, err := cfg.PeerURL()
	if err != nil {
		return nil, err
	}
	return syncer.New(ctx, n, agent.NewClient(u, cfg.Token))
}

// selectRepos returns the named repos, or all of them.
func selectRepos(cfg *config.Config, names []string) ([]string, error) {
	if len(names) == 0 {
		for _, r := range cfg.Repos {
			names = append(names, r.Name)
		}
		if len(names) == 0 {
			return nil, errors.New("no repos in config yet; add [[repo]] entries")
		}
		return names, nil
	}
	for _, n := range names {
		if _, ok := cfg.Repo(n); !ok {
			return nil, fmt.Errorf("%s: %w", n, node.ErrUnknownRepo)
		}
	}
	return names, nil
}

func cmdStatus(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.Parse(args)
	cfg, n, err := load(cfgPath)
	if err != nil {
		return err
	}
	names, err := selectRepos(cfg, fs.Args())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer tw.Flush()

	s, err := connect(ctx, cfg, n)
	if err != nil {
		fmt.Printf("peer unavailable: %v\n\n", err)
		fmt.Fprintf(tw, "REPO\t%s\n", strings.ToUpper(cfg.Host))
		for _, name := range names {
			st, err := n.State(ctx, name)
			if err != nil {
				fmt.Fprintf(tw, "%s\t%v\n", name, err)
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\n", name, st.Label())
		}
		return nil
	}
	fmt.Fprintf(tw, "REPO\t%s\t%s\tACTION\n", strings.ToUpper(s.LocalHost), strings.ToUpper(s.PeerHost))
	for _, name := range names {
		p := s.Plan(ctx, name, syncer.None)
		local, peer := p.Local.Label(), p.Peer.Label()
		if p.Local.Head == "" {
			local = "-"
		}
		if p.Peer.Head == "" {
			peer = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, local, peer, describe(s, p))
	}
	return nil
}

func describe(s *syncer.Syncer, p syncer.Plan) string {
	switch p.Action {
	case syncer.InSync:
		return "ok"
	case syncer.Pull:
		return "<- pull: " + p.Reason
	case syncer.Push:
		return "-> push: " + p.Reason
	case syncer.Conflict, syncer.Choose:
		return fmt.Sprintf("!! %s: %s (use --take %s|%s)", p.Action, p.Reason, s.LocalHost, s.PeerHost)
	default:
		return "error: " + p.Err.Error()
	}
}

func cmdSync(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	take := fs.String("take", "", "when both sides changed (or never synced), make this machine's copy win: a host name, 'local' or 'peer'")
	fs.Parse(args)
	cfg, n, err := load(cfgPath)
	if err != nil {
		return err
	}
	names, err := selectRepos(cfg, fs.Args())
	if err != nil {
		return err
	}
	s, err := connect(ctx, cfg, n)
	if err != nil {
		return err
	}
	side := syncer.None
	switch strings.ToLower(*take) {
	case "":
	case "local", strings.ToLower(s.LocalHost):
		side = syncer.LocalSide
	case "peer", strings.ToLower(s.PeerHost):
		side = syncer.PeerSide
	default:
		return fmt.Errorf("--take %q: expected %s or %s", *take, s.LocalHost, s.PeerHost)
	}

	failed := 0
	for _, name := range names {
		_, msg, err := s.Sync(ctx, name, side)
		if err != nil {
			failed++
			fmt.Printf("%-20s error: %v\n", name, err)
			continue
		}
		fmt.Printf("%-20s %s\n", name, msg)
	}
	if failed > 0 {
		return fmt.Errorf("%d repo(s) failed", failed)
	}
	return nil
}

func cmdBootstrap(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	fs.Parse(args)
	cfg, _, err := load(cfgPath)
	if err != nil {
		return err
	}
	for _, r := range cfg.Repos {
		dir := cfg.RepoDir(r)
		if _, err := os.Stat(dir); err == nil {
			fmt.Printf("%-20s present at %s\n", r.Name, dir)
			continue
		}
		if r.URL == "" {
			fmt.Printf("%-20s missing, and no url in config to clone from\n", r.Name)
			continue
		}
		fmt.Printf("%-20s cloning %s\n", r.Name, r.URL)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		c := exec.Command("git", "clone", r.URL, dir)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("cloning %s: %w", r.Name, err)
		}
	}
	fmt.Println("\nThe next `reposync sync` brings over branches and uncommitted work from the other machine.")
	return nil
}

func cmdBackups(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("backups", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: reposync backups REPO")
	}
	_, n, err := load(cfgPath)
	if err != nil {
		return err
	}
	all, err := n.Backups(fs.Arg(0))
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println("no backups yet")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	defer tw.Flush()
	fmt.Fprintln(tw, "ID\tSTATE")
	for _, b := range all {
		fmt.Fprintf(tw, "%s\t%s\n", b.ID, b.State.Label())
	}
	return nil
}

func cmdUndo(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("undo", flag.ExitOnError)
	to := fs.String("to", "", "backup ID to restore (default: the newest)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: reposync undo [--to ID] REPO")
	}
	_, n, err := load(cfgPath)
	if err != nil {
		return err
	}
	b, s, err := n.Undo(fs.Arg(0), *to)
	if err != nil {
		return err
	}
	fmt.Printf("restored backup %s, now %s\n", b.ID, s.Label())
	fmt.Println("The current state was backed up too, so this undo can itself be undone.")
	fmt.Println("The next sync of this repo will ask which machine wins (--take).")
	return nil
}

func cmdDoctor(ctx context.Context, cfgPath string, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	fs.Parse(args)
	cfg, n, err := load(cfgPath)
	if err != nil {
		return err
	}
	local, err := n.Doctor(ctx)
	if err != nil {
		return err
	}
	var peer *node.Report
	var peerErr error
	if u, err := cfg.PeerURL(); err != nil {
		peerErr = err
	} else if rep, err := agent.NewClient(u, cfg.Token).Doctor(ctx); err != nil {
		peerErr = err
	} else {
		peer = &rep
	}

	findings := doctor.Check(&local, peer)
	if peerErr != nil {
		findings = append(findings, doctor.Finding{Level: doctor.Fail, Text: "peer: " + peerErr.Error(),
			Advice: "is reposync (serve or the tray app) running on the other machine, and allowed through its firewall?"})
	}
	if v, _ := desktop.Autostart(); v == "" {
		findings = append(findings, doctor.Finding{Level: doctor.Warn, Text: "the tray app doesn't start at login", Advice: "reposync autostart on"})
	}
	failed := false
	for _, f := range findings {
		where := ""
		if f.Repo != "" {
			where = f.Repo + ": "
		}
		fmt.Printf("[%s] %s%s\n", f.Level, where, f.Text)
		if f.Advice != "" {
			fmt.Printf("       -> %s\n", f.Advice)
		}
		failed = failed || f.Level == doctor.Fail
	}
	if failed {
		return errors.New("problems found")
	}
	fmt.Println("\nAll good.")
	return nil
}

func cmdAutostart(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off" && args[0] != "status") {
		return errors.New("usage: reposync autostart on|off|status")
	}
	if args[0] != "status" {
		if err := desktop.SetAutostart(args[0] == "on"); err != nil {
			return err
		}
	}
	v, err := desktop.Autostart()
	if err != nil {
		return err
	}
	if v == "" {
		fmt.Println("autostart is off")
	} else {
		fmt.Println("autostart is on:", v)
	}
	return nil
}
