// Package deletions tracks the repositories the WebUI is deleting.
//
// Lore unregisters a repository from the control plane (RepositoryUnregister) before it deletes
// its own data. If the control plane dropped its row then, a deletion that failed half way would
// leave a repository the WebUI no longer lists, whose name Lore still holds, with nowhere to try
// again. While the WebUI deletes a repository, the row stays until Lore confirms the deletion,
// and a failure leaves it listed so the deletion can be repeated.
package deletions

import "sync"

// Tracker is safe for concurrent use. The zero value is ready.
type Tracker struct {
	mu  sync.Mutex
	ids map[string]int
}

// Begin marks repository id as being deleted by the WebUI until the returned function is called.
// A nil tracker tracks nothing.
func (t *Tracker) Begin(id string) (done func()) {
	if t == nil {
		return func() {}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ids == nil {
		t.ids = map[string]int{}
	}
	t.ids[id]++
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.ids[id]--; t.ids[id] <= 0 {
				delete(t.ids, id)
			}
		})
	}
}

// Active reports whether the WebUI is deleting repository id. A nil tracker tracks nothing.
func (t *Tracker) Active(id string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ids[id] > 0
}
