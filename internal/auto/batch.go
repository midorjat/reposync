package auto

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Arrivals collects "work from <host> arrived in <repo>" events and sends
// one notification per burst, so syncing five repos doesn't pop five toasts.
type Arrivals struct {
	Notify Notifier
	Delay  time.Duration

	mu    sync.Mutex
	repos map[string]map[string]bool // host -> repos
	timer *time.Timer
}

func (b *Arrivals) Add(from, repo string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.repos == nil {
		b.repos = map[string]map[string]bool{}
	}
	if b.repos[from] == nil {
		b.repos[from] = map[string]bool{}
	}
	b.repos[from][repo] = true
	if b.timer == nil {
		b.timer = time.AfterFunc(b.Delay, b.flush)
	}
}

func (b *Arrivals) flush() {
	b.mu.Lock()
	pending := b.repos
	b.repos, b.timer = nil, nil
	b.mu.Unlock()
	for host, set := range pending {
		var names []string
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		b.Notify.Notify("Work from "+host+" arrived",
			fmt.Sprintf("%s %s now as you left %s on %s.", strings.Join(names, ", "), isAre(len(names)), itThem(len(names)), host))
	}
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func itThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
