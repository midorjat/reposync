package node

import (
	"context"
	"os/exec"
	"runtime"
	"strings"

	"reposync/internal/repo"
)

// Git settings that change how files on disk map to what git stores. If they
// differ between the machines, the same files snapshot differently.
var DoctorSettings = []string{"core.autocrlf", "core.eol", "core.ignorecase", "core.symlinks", "core.filemode", "core.longpaths"}

// Report describes this machine for `reposync doctor`.
type Report struct {
	Host       string       `json:"host"`
	Version    string       `json:"version"`
	OS         string       `json:"os"`
	GitVersion string       `json:"gitVersion"`
	Repos      []RepoReport `json:"repos"`
}

type RepoReport struct {
	Name     string            `json:"name"`
	Dir      string            `json:"dir"`
	Err      string            `json:"err,omitempty"` // missing, no commits, busy, ...
	Origin   string            `json:"origin,omitempty"`
	Lease    string            `json:"lease,omitempty"` // who holds the sync lock, if anyone
	Settings map[string]string `json:"settings,omitempty"`
}

func (n *Node) Doctor(context.Context) (Report, error) {
	r := Report{Host: n.Cfg.Host, Version: Version, OS: runtime.GOOS}
	if out, err := exec.Command("git", "--version").Output(); err == nil {
		r.GitVersion = strings.TrimSpace(strings.TrimPrefix(string(out), "git version "))
	}
	for _, cr := range n.Cfg.Repos {
		rr := RepoReport{Name: cr.Name, Dir: n.Cfg.RepoDir(cr)}
		dir, err := n.Dir(cr.Name)
		if err != nil {
			rr.Err = err.Error()
			r.Repos = append(r.Repos, rr)
			continue
		}
		g := repo.Git{Dir: dir}
		if _, err := n.State(context.Background(), cr.Name); err != nil {
			rr.Err = err.Error()
		}
		rr.Origin, _ = g.Run("config", "--get", "remote.origin.url")
		rr.Lease, _ = repo.LeaseHolder(dir)
		rr.Settings = map[string]string{}
		for _, key := range DoctorSettings {
			v, _ := g.Run("config", "--get", key) // unset is an error; record ""
			rr.Settings[key] = strings.ToLower(v)
		}
		r.Repos = append(r.Repos, rr)
	}
	return r, nil
}
