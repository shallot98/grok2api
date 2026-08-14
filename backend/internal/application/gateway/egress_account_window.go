package gateway

import (
	"sync"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
)

type egressAccountWindow struct {
	mu          sync.Mutex
	duration    time.Duration
	maxDistinct int
	accounts    map[uint64]map[uint64]time.Time
}

func (w *egressAccountWindow) update(duration time.Duration, maxDistinct int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if duration == w.duration && maxDistinct == w.maxDistinct {
		return
	}
	w.duration = duration
	w.maxDistinct = maxDistinct
	w.accounts = make(map[uint64]map[uint64]time.Time)
}

func (w *egressAccountWindow) allows(value account.Credential, now time.Time) (bool, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.evaluate(value, now, false)
}

func (w *egressAccountWindow) record(value account.Credential, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	allowed, _ := w.evaluate(value, now, true)
	return allowed
}

func (w *egressAccountWindow) evaluate(value account.Credential, now time.Time, record bool) (bool, time.Duration) {
	if value.Provider != account.ProviderBuild || value.EgressNodeID == 0 || w.duration <= 0 || w.maxDistinct <= 0 {
		return true, 0
	}
	if w.accounts == nil {
		w.accounts = make(map[uint64]map[uint64]time.Time)
	}
	accounts := w.accounts[value.EgressNodeID]
	if accounts == nil {
		accounts = make(map[uint64]time.Time)
		w.accounts[value.EgressNodeID] = accounts
	}
	cutoff := now.Add(-w.duration)
	for accountID, seenAt := range accounts {
		if !seenAt.After(cutoff) {
			delete(accounts, accountID)
		}
	}
	if _, exists := accounts[value.ID]; exists {
		if record {
			accounts[value.ID] = now
		}
		return true, 0
	}
	if len(accounts) < w.maxDistinct {
		if record {
			accounts[value.ID] = now
		}
		return true, 0
	}
	earliest := now
	for _, seenAt := range accounts {
		if earliest.Equal(now) || seenAt.Before(earliest) {
			earliest = seenAt
		}
	}
	return false, max(time.Millisecond, earliest.Add(w.duration).Sub(now))
}
