package doctor

import (
	"strings"
	"testing"

	"reposync/internal/node"
)

func report(host string, repos ...node.RepoReport) *node.Report {
	return &node.Report{Host: host, Version: "0.2.0", OS: "windows", GitVersion: "2.45.1.windows.1", Repos: repos}
}

func find(fs []Finding, l Level, sub string) bool {
	for _, f := range fs {
		if f.Level == l && strings.Contains(f.Text, sub) {
			return true
		}
	}
	return false
}

func TestCheck(t *testing.T) {
	on := map[string]string{"core.longpaths": "true"}
	a := report("laptop",
		node.RepoReport{Name: "api", Origin: "o", Settings: map[string]string{"core.autocrlf": "true", "core.filemode": "", "core.longpaths": "true"}},
		node.RepoReport{Name: "web", Origin: "w", Settings: on},
		node.RepoReport{Name: "only-here", Settings: on})
	b := report("desktop",
		node.RepoReport{Name: "api", Origin: "o", Settings: map[string]string{"core.autocrlf": "input", "core.filemode": "true"}},
		node.RepoReport{Name: "web", Origin: "w", Settings: map[string]string{"core.autocrlf": "false", "core.longpaths": "true"}})
	fs := Check(a, b)

	if !find(fs, Fail, "core.autocrlf differs: laptop=true, desktop=input") {
		t.Errorf("autocrlf mismatch not reported: %+v", fs)
	}
	if find(fs, Warn, "core.filemode") {
		t.Error("unset filemode means true; should not be reported")
	}
	if !find(fs, OK, "set up the same") {
		t.Error("web: unset autocrlf equals false; should be OK")
	}
	if !find(fs, Fail, "only in laptop's config") {
		t.Error("repo missing on the peer not reported")
	}
	if !find(fs, Warn, "desktop: core.longpaths is off") {
		t.Error("longpaths warning missing")
	}
}

func TestGitAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"2.43.0": true, "2.29.0": true, "2.28.9": false, "2.45.1.windows.1": true, "3.0": true, "junk": false} {
		if got := gitAtLeast(v, minGit); got != want {
			t.Errorf("%s: got %v", v, got)
		}
	}
}
