// Package doctor checks that both machines are set up so syncing works:
// same repos, same git settings, nothing stuck.
package doctor

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"reposync/internal/node"
)

type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) String() string { return [...]string{"ok  ", "warn", "FAIL"}[l] }

type Finding struct {
	Level  Level
	Repo   string // "" for machine-wide findings
	Text   string
	Advice string
}

// minGit is the oldest git with every flag reposync uses (fetch --no-write-fetch-head).
var minGit = [2]int{2, 29}

// Check looks at this machine and, if peer is not nil, compares the two.
func Check(local *node.Report, peer *node.Report) []Finding {
	var out []Finding
	add := func(l Level, repo, text, advice string) {
		out = append(out, Finding{Level: l, Repo: repo, Text: text, Advice: advice})
	}

	for _, r := range reports(local, peer) {
		switch {
		case r.GitVersion == "":
			add(Fail, "", r.Host+": git not found", "install Git for Windows and make sure git is on PATH")
		case !gitAtLeast(r.GitVersion, minGit):
			add(Fail, "", fmt.Sprintf("%s: git %s is too old", r.Host, r.GitVersion), fmt.Sprintf("update to git %d.%d or newer", minGit[0], minGit[1]))
		default:
			add(OK, "", fmt.Sprintf("%s: git %s, reposync %s", r.Host, r.GitVersion, r.Version), "")
		}
		for _, rr := range r.Repos {
			if rr.Err != "" {
				add(Fail, rr.Name, r.Host+": "+rr.Err, "")
			}
			if rr.Lease != "" {
				add(Warn, rr.Name, r.Host+": sync lock held by "+rr.Lease, "if no sync is running it expires within 15 minutes")
			}
			if r.OS == "windows" && rr.Err == "" && rr.Settings["core.longpaths"] != "true" {
				add(Warn, rr.Name, r.Host+": core.longpaths is off", "git config --global core.longpaths true")
			}
		}
	}
	if peer == nil {
		return out
	}

	if local.Version != peer.Version {
		add(Warn, "", fmt.Sprintf("reposync versions differ (%s %s, %s %s)", local.Host, local.Version, peer.Host, peer.Version), "use the same version on both machines")
	}
	mine, theirs := byName(local), byName(peer)
	for _, name := range union(mine, theirs) {
		a, inA := mine[name]
		b, inB := theirs[name]
		switch {
		case !inA:
			add(Fail, name, "only in "+peer.Host+"'s config", "add it to "+local.Host+"'s config too")
			continue
		case !inB:
			add(Fail, name, "only in "+local.Host+"'s config", "add it to "+peer.Host+"'s config too")
			continue
		case a.Err != "" || b.Err != "":
			continue // already reported
		}
		same := true
		for _, key := range node.DoctorSettings {
			if key == "core.longpaths" || norm(key, a.Settings[key]) == norm(key, b.Settings[key]) {
				continue
			}
			same = false
			level := Warn
			if key == "core.autocrlf" || key == "core.eol" {
				level = Fail // every file would look changed after a sync
			}
			add(level, name, fmt.Sprintf("%s differs: %s=%s, %s=%s", key, local.Host, show(a.Settings[key]), peer.Host, show(b.Settings[key])),
				fmt.Sprintf("make them equal, e.g. git config --global %s <value> on one machine", key))
		}
		if a.Origin != b.Origin {
			add(Warn, name, fmt.Sprintf("origin differs: %s vs %s", show(a.Origin), show(b.Origin)), "are these really the same repo?")
		} else if same {
			add(OK, name, "set up the same on both machines", "")
		}
	}
	return out
}

func reports(rs ...*node.Report) []*node.Report {
	var out []*node.Report
	for _, r := range rs {
		if r != nil {
			out = append(out, r)
		}
	}
	return out
}

func byName(r *node.Report) map[string]node.RepoReport {
	m := map[string]node.RepoReport{}
	for _, rr := range r.Repos {
		m[rr.Name] = rr
	}
	return m
}

func union(a, b map[string]node.RepoReport) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// gitDefaults are what an unset setting means, so "unset" and "false" compare equal.
var gitDefaults = map[string]string{
	"core.autocrlf": "false", "core.ignorecase": "false", "core.symlinks": "true", "core.filemode": "true",
}

func norm(key, v string) string {
	if v == "" {
		return gitDefaults[key]
	}
	return v
}

func show(v string) string {
	if v == "" {
		return "(unset)"
	}
	return v
}

// gitAtLeast parses versions like "2.43.0" or "2.45.1.windows.1".
func gitAtLeast(v string, min [2]int) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return major > min[0] || (major == min[0] && minor >= min[1])
}
