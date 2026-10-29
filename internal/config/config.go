// Package config loads the per-machine reposync configuration, which also
// carries the list of repos to keep in sync (the manifest).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const DefaultPort = "7420"

type Repo struct {
	Name string `toml:"name"`
	Path string `toml:"path"`
	URL  string `toml:"url"`
}

type Config struct {
	Host   string `toml:"host"`
	Listen string `toml:"listen"`
	Peer   string `toml:"peer"`
	Token  string `toml:"token"`
	Root   string `toml:"root"`
	Auto   Auto   `toml:"auto"`
	Repos  []Repo `toml:"repo"`
}

// Auto configures what the tray agent does on its own.
type Auto struct {
	Enabled  *bool  `toml:"enabled"`  // default true
	Interval string `toml:"interval"` // re-check while the peer is reachable; default 10m
	Notify   *bool  `toml:"notify"`   // default true
}

func (a Auto) On() bool        { return a.Enabled == nil || *a.Enabled }
func (a Auto) Notifying() bool { return a.Notify == nil || *a.Notify }

func (a Auto) Every() time.Duration {
	d, err := time.ParseDuration(a.Interval)
	if err != nil || d < time.Minute {
		return 10 * time.Minute
	}
	return d
}

// Names end up in URLs and git ref names, so keep them plain.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// DefaultPath is %APPDATA%\reposync\config.toml on Windows,
// ~/.config/reposync/config.toml on Linux. REPOSYNC_CONFIG overrides it.
func DefaultPath() (string, error) {
	if p := os.Getenv("REPOSYNC_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "reposync", "config.toml"), nil
}

func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no config at %s (run `reposync init` first)", path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if c.Listen == "" {
		c.Listen = ":" + DefaultPort
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) Validate() error {
	if !nameRE.MatchString(c.Host) || strings.EqualFold(c.Host, "self") {
		return fmt.Errorf("host %q must be a plain name like \"laptop\"", c.Host)
	}
	if c.Token == "" {
		return fmt.Errorf("token is empty")
	}
	if c.Auto.Interval != "" {
		if _, err := time.ParseDuration(c.Auto.Interval); err != nil {
			return fmt.Errorf("auto.interval %q: use a duration like '10m' or '1h'", c.Auto.Interval)
		}
	}
	seen := map[string]bool{}
	for _, r := range c.Repos {
		if !nameRE.MatchString(r.Name) {
			return fmt.Errorf("repo name %q may only use letters, digits, '.', '_' and '-'", r.Name)
		}
		if seen[r.Name] {
			return fmt.Errorf("repo %q is listed twice", r.Name)
		}
		seen[r.Name] = true
		if r.Path == "" {
			return fmt.Errorf("repo %q has no path", r.Name)
		}
	}
	return nil
}

func (c *Config) Repo(name string) (Repo, bool) {
	for _, r := range c.Repos {
		if r.Name == name {
			return r, true
		}
	}
	return Repo{}, false
}

// RepoDir resolves a repo's path; relative paths are taken from root.
func (c *Config) RepoDir(r Repo) string {
	p := expandHome(r.Path)
	if filepath.IsAbs(p) || c.Root == "" {
		return filepath.Clean(p)
	}
	return filepath.Join(expandHome(c.Root), p)
}

// PeerURL accepts "desktop", "desktop:7420" or a full http:// URL.
func (c *Config) PeerURL() (string, error) {
	p := strings.TrimRight(c.Peer, "/")
	if p == "" {
		return "", fmt.Errorf("no peer set in config")
	}
	if !strings.Contains(p, "://") {
		p = "http://" + p
	}
	rest := p[strings.Index(p, "://")+3:]
	if !strings.Contains(rest, ":") {
		p += ":" + DefaultPort
	}
	return p, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// Template is the starting config written by `reposync init`. TOML literal
// strings ('...') are used so Windows paths need no escaping.
func Template(host, peer, root, token string) string {
	return fmt.Sprintf(`# reposync config for this machine.
# Copy the [[repo]] list between machines; host/peer differ per machine.

host  = '%s'           # this machine's name
peer  = '%s'           # the other machine: name, name:port or http://name:port
token = '%s'           # must be identical on both machines - keep it secret
root  = '%s'           # relative repo paths are resolved from here
# listen = ':%s'

# What the tray app (reposync-tray) does on its own. It syncs when the other
# machine shows up on the network, after waking from sleep, and every
# 'interval' while both are reachable. Conflicts always wait for you.
[auto]
enabled  = true
interval = '10m'
notify   = true

# [[repo]]
# name = 'api'                              # same name on both machines
# path = 'work\api'                         # relative to root, or absolute
# url  = 'git@github.com:you/api.git'       # used by: reposync bootstrap
`, host, peer, token, root, DefaultPort)
}
