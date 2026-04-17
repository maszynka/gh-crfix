// Package shutdown provides signal-aware shutdown helpers.
//
// It mirrors the bash pattern used by the original gh-crfix script, where an
// EXIT + INT/TERM trap runs a cleanup function. WithSignals returns a context
// that cancels on SIGINT/SIGTERM, and CleanupRegistry collects LIFO cleanup
// callbacks with a per-function time budget.
package shutdown

import (
	"context"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// cleanupBudget is the wall-clock budget granted to each cleanup function
// inside RunAll. Matches the docstring contract (~5s per func).
const cleanupBudget = 5 * time.Second

// WithSignals returns a context cancelled on SIGINT/SIGTERM plus a cleanup
// func. Callers MUST call the cleanup func (typically via defer) so the
// signal watcher goroutine is released. Dropping the cleanup leaks the
// watcher — by design, not worked around.
func WithSignals(parent context.Context) (context.Context, func()) {
	// signal.NotifyContext is the stdlib primitive that does exactly what
	// we want: registers handlers for the given signals and cancels the
	// returned context on delivery.
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	return ctx, stop
}

// entry is one registered cleanup callback.
type entry struct {
	id   uint64
	name string
	fn   func()
}

// CleanupRegistry is a thread-safe LIFO registry of cleanup callbacks.
// Each callback gets a ~5s budget when RunAll is invoked; callbacks that
// exceed their budget are abandoned (leaked goroutine) so RunAll can make
// forward progress.
type CleanupRegistry struct {
	mu      sync.Mutex
	entries []entry
	nextID  uint64
	ran     bool
}

// NewCleanupRegistry returns a fresh, empty registry.
func NewCleanupRegistry() *CleanupRegistry {
	return &CleanupRegistry{}
}

// Register adds fn to the registry under name. Returns an unregister func
// that removes the entry (noop if the entry is already gone, e.g. because
// RunAll consumed it).
func (r *CleanupRegistry) Register(name string, fn func()) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := r.nextID
	r.entries = append(r.entries, entry{id: id, name: name, fn: fn})
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, e := range r.entries {
			if e.id == id {
				r.entries = append(r.entries[:i], r.entries[i+1:]...)
				return
			}
		}
	}
}

// RunAll invokes every registered cleanup in reverse registration order
// (LIFO). Each callback is granted cleanupBudget; callbacks that exceed
// the budget are abandoned so RunAll can finish in bounded time. After
// RunAll returns the registry is empty and subsequent calls are no-ops.
func (r *CleanupRegistry) RunAll() {
	r.mu.Lock()
	if r.ran {
		r.mu.Unlock()
		return
	}
	r.ran = true
	// Take ownership of entries and drain the registry.
	entries := r.entries
	r.entries = nil
	r.mu.Unlock()

	// LIFO: walk the slice in reverse.
	for i := len(entries) - 1; i >= 0; i-- {
		runWithBudget(entries[i].fn, cleanupBudget)
	}
}

// runWithBudget runs fn, returning when it completes or after d elapses.
// If fn outruns the budget it is abandoned (the goroutine keeps running
// but no one is waiting on it) — this matches the documented contract.
func runWithBudget(fn func(), d time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Defensive: swallow panics so one bad cleanup doesn't take
		// down the whole shutdown path.
		defer func() { _ = recover() }()
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}
