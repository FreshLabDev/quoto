// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"sync"
	"time"
)

const (
	// throttleWindow is how soon the same person may press another button of the
	// same panel. A second press inside it gets an empty answer and does nothing.
	throttleWindow = 700 * time.Millisecond
	// throttlePruneAt is the size at which stale entries are dropped.
	throttlePruneAt = 1024
	// throttleStale is how old an entry must be to be dropped.
	throttleStale = 60 * time.Second
)

type throttleKey struct{ chat, message, user int64 }

// callbackThrottle remembers when each (panel, person) pair last pressed a button.
type callbackThrottle struct {
	mu   sync.Mutex
	seen map[throttleKey]time.Time
}

func (t *callbackThrottle) init() { t.seen = map[throttleKey]time.Time{} }

// hit records a press and reports whether it came too soon after the last one.
// A press that carries no panel or no person cannot be told apart from another,
// so it is never throttled.
func (t *callbackThrottle) hit(chat, message, user int64, now time.Time) bool {
	if chat == 0 || message == 0 || user == 0 {
		return false
	}
	key := throttleKey{chat, message, user}
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.seen[key]; ok && now.Sub(last) < throttleWindow {
		return true
	}
	t.seen[key] = now
	if len(t.seen) > throttlePruneAt {
		for k, at := range t.seen {
			if now.Sub(at) > throttleStale {
				delete(t.seen, k)
			}
		}
		// Every entry is fresh only under a flood of presses. Forgetting them
		// lets a few through, which costs less than a map that keeps growing.
		if len(t.seen) > 2*throttlePruneAt {
			clear(t.seen)
		}
	}
	return false
}

const (
	// panelTTL is how long a panel is remembered. Telegram stops delivering
	// buttons of a message older than about two days.
	panelTTL = 48 * time.Hour
	// panelMax bounds the registry.
	panelMax = 2048
)

type msgRef struct{ chat, message int64 }

type panelEntry struct {
	command msgRef
	at      time.Time
}

// panelRegistry remembers which /start message opened which group panel, so that
// Close can remove both. It is bounded and entries expire, unlike the map it
// replaces, which grew with every panel ever opened and was lost on restart.
type panelRegistry struct {
	mu sync.Mutex
	m  map[msgRef]panelEntry
}

func (r *panelRegistry) init() { r.m = map[msgRef]panelEntry{} }

func (r *panelRegistry) register(panel, command msgRef, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.m) >= panelMax {
		r.prune(now)
	}
	r.m[panel] = panelEntry{command: command, at: now}
}

// take returns the command message of a panel and forgets the panel.
func (r *panelRegistry) take(panel msgRef, now time.Time) (msgRef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[panel]
	if !ok {
		return msgRef{}, false
	}
	delete(r.m, panel)
	if now.Sub(e.at) > panelTTL {
		return msgRef{}, false
	}
	return e.command, true
}

func (r *panelRegistry) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.m)
}

// prune drops the expired entries and, if that is not enough, the oldest ones.
func (r *panelRegistry) prune(now time.Time) {
	for k, e := range r.m {
		if now.Sub(e.at) > panelTTL {
			delete(r.m, k)
		}
	}
	for len(r.m) >= panelMax {
		var oldest msgRef
		var at time.Time
		first := true
		for k, e := range r.m {
			if first || e.at.Before(at) {
				oldest, at, first = k, e.at, false
			}
		}
		delete(r.m, oldest)
	}
}
