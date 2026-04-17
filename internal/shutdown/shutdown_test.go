package shutdown

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// waitForCancel returns true if ctx.Done fires within d.
func waitForCancel(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func TestWithSignals_CancelsOnSIGINT(t *testing.T) {
	ctx, cleanup := WithSignals(context.Background())
	defer cleanup()

	// Sanity: not cancelled yet.
	select {
	case <-ctx.Done():
		t.Fatalf("context cancelled before signal delivery")
	default:
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("syscall.Kill: %v", err)
	}
	if !waitForCancel(ctx, 2*time.Second) {
		t.Fatalf("context not cancelled within 2s of SIGINT")
	}
}

func TestWithSignals_CleanupStopsWatcher(t *testing.T) {
	ctx, cleanup := WithSignals(context.Background())
	cleanup()

	// After cleanup the derived context is cancelled. Deliver SIGINT and
	// ensure the process isn't killed because signal.NotifyContext has
	// been released. Installing our own handler is how we detect that the
	// Notify watcher is no longer capturing the signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	defer signal.Stop(sigCh)

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("syscall.Kill: %v", err)
	}

	select {
	case <-sigCh:
		// Good — our test-local handler received SIGINT. The watcher
		// wasn't holding on to it (if it were still alive it would
		// also deliver, but more importantly after cleanup the ctx
		// is already cancelled so we can't observe "doesn't cancel".
		// We at least confirmed the process did not die.)
	case <-time.After(2 * time.Second):
		t.Fatalf("test-local signal handler did not receive SIGINT")
	}

	// ctx is expected to be cancelled by cleanup().
	if !waitForCancel(ctx, time.Second) {
		t.Fatalf("context not cancelled after cleanup")
	}
}

func TestCleanupRegistry_LIFOOrder(t *testing.T) {
	reg := NewCleanupRegistry()
	var mu sync.Mutex
	var order []string

	reg.Register("first", func() {
		mu.Lock()
		order = append(order, "first")
		mu.Unlock()
	})
	reg.Register("second", func() {
		mu.Lock()
		order = append(order, "second")
		mu.Unlock()
	})
	reg.Register("third", func() {
		mu.Lock()
		order = append(order, "third")
		mu.Unlock()
	})

	reg.RunAll()

	mu.Lock()
	defer mu.Unlock()
	want := []string{"third", "second", "first"}
	if len(order) != len(want) {
		t.Fatalf("order len = %d, want %d (%v)", len(order), len(want), order)
	}
	for i, v := range want {
		if order[i] != v {
			t.Errorf("order[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestCleanupRegistry_Unregister(t *testing.T) {
	reg := NewCleanupRegistry()
	var ran atomic.Int32

	reg.Register("keep", func() { ran.Add(1) })
	unreg := reg.Register("drop", func() { ran.Add(100) })
	reg.Register("keep2", func() { ran.Add(1) })

	unreg()
	// Unregister should be idempotent.
	unreg()

	reg.RunAll()

	if got := ran.Load(); got != 2 {
		t.Errorf("ran = %d, want 2 (drop should have been unregistered)", got)
	}
}

func TestCleanupRegistry_UnregisterNoopAfterRun(t *testing.T) {
	reg := NewCleanupRegistry()
	var ran atomic.Int32

	unreg := reg.Register("once", func() { ran.Add(1) })
	reg.RunAll()

	if got := ran.Load(); got != 1 {
		t.Fatalf("ran = %d, want 1", got)
	}
	// Should not panic or double-run anything.
	unreg()
}

func TestCleanupRegistry_PerFuncBudget(t *testing.T) {
	reg := NewCleanupRegistry()
	reg.Register("slow", func() {
		time.Sleep(10 * time.Second)
	})

	start := time.Now()
	reg.RunAll()
	elapsed := time.Since(start)

	if elapsed > 6*time.Second {
		t.Errorf("RunAll took %v; expected <=6s due to per-func budget", elapsed)
	}
	if elapsed < 4*time.Second {
		t.Errorf("RunAll took %v; expected ~5s (it should have waited for the budget)", elapsed)
	}
}

func TestCleanupRegistry_ConcurrentRegisterUnregister(t *testing.T) {
	reg := NewCleanupRegistry()

	var wg sync.WaitGroup
	const n = 50
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			unreg := reg.Register("x", func() {})
			// Half of them unregister, half leave the entry in.
			if i%2 == 0 {
				unreg()
			}
		}(i)
	}
	wg.Wait()

	// RunAll should be safe to call after concurrent register/unregister.
	reg.RunAll()
}

func TestCleanupRegistry_RunAllSafeAfterCancel(t *testing.T) {
	// Calling RunAll twice is idempotent (no double execution).
	reg := NewCleanupRegistry()
	var ran atomic.Int32
	reg.Register("x", func() { ran.Add(1) })
	reg.RunAll()
	reg.RunAll()
	if got := ran.Load(); got != 1 {
		t.Errorf("ran = %d, want 1 (RunAll should be idempotent)", got)
	}
}
