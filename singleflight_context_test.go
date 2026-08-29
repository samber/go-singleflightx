package singleflightx

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoContext(t *testing.T) {
	var g Group[string, string]
	v, err, shared := g.DoContext(context.Background(), "key", func(ctx context.Context) (string, error) {
		return "bar", nil
	})
	if v != "bar" {
		t.Errorf("DoContext = %v; want bar", v)
	}
	if err != nil {
		t.Errorf("DoContext error = %v", err)
	}
	if shared {
		t.Errorf("DoContext shared = true; want false")
	}
}

func TestDoChanContext(t *testing.T) {
	var g Group[string, string]
	ch := g.DoChanContext(context.Background(), "key", func(ctx context.Context) (string, error) {
		return "bar", nil
	})

	res := <-ch
	if res.Value.Value != "bar" || !res.Value.Valid || res.Err != nil {
		t.Errorf("unexpected result: %+v", res)
	}
}

// A caller whose own context expires must be unblocked with its context's
// error, while every other caller attached to the same key keeps waiting for
// the real result, and fn itself keeps running (its own context is not
// canceled).
func TestDoContextPartialTimeout(t *testing.T) {
	var g Group[string, int]

	fnStarted := make(chan struct{})
	unblockFn := make(chan struct{})
	var fnCtx context.Context

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		v, err, _ := g.DoContext(context.Background(), "key", func(ctx context.Context) (int, error) {
			fnCtx = ctx
			close(fnStarted)
			<-unblockFn
			return 42, nil
		})
		if err != nil || v != 42 {
			t.Errorf("leader DoContext = %v, %v; want 42, nil", v, err)
		}
	}()
	<-fnStarted

	// This caller times out well before the leader unblocks fn.
	ctxB, cancelB := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelB()
	_, errB, sharedB := g.DoContext(ctxB, "key", func(ctx context.Context) (int, error) {
		t.Error("fn should not be invoked again for a joining caller")
		return 0, nil
	})
	if !errors.Is(errB, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", errB)
	}
	if !sharedB {
		t.Errorf("expected shared=true, the leader is still attached")
	}

	if fnCtx.Err() != nil {
		t.Errorf("fn's context should not be canceled while the leader is still attached")
	}

	close(unblockFn)
	<-leaderDone
}

// Once the sole caller's context expires, fn's own context must be canceled.
func TestDoContextLastCallerTimeoutCancelsFn(t *testing.T) {
	var g Group[string, int]

	fnCtxCanceled := make(chan struct{})
	callerDone := make(chan struct{})

	callerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	go func() {
		defer close(callerDone)
		_, err, _ := g.DoContext(callerCtx, "key", func(ctx context.Context) (int, error) {
			<-ctx.Done() // fn's own context, canceled once every caller has left
			close(fnCtxCanceled)
			return 0, nil
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expected DeadlineExceeded, got %v", err)
		}
	}()

	select {
	case <-fnCtxCanceled:
	case <-time.After(time.Second):
		t.Fatal("fn's context was never canceled")
	}

	select {
	case <-callerDone:
	case <-time.After(time.Second):
		t.Fatal("DoContext did not return")
	}
}

// Once every caller of a key has left, the key must be free for a fresh
// call rather than joining the still-running (but unwanted) one.
func TestDoContextNewCallAfterFullCancel(t *testing.T) {
	var g Group[string, int]

	firstFnStarted := make(chan struct{})
	firstUnblock := make(chan struct{})

	callerCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		g.DoContext(callerCtx, "key", func(ctx context.Context) (int, error) { //nolint:errcheck
			close(firstFnStarted)
			<-firstUnblock
			return 1, nil
		})
	}()
	<-firstFnStarted
	<-firstDone // the sole caller timed out; the key must now be free

	secondCalled := false
	v, err, shared := g.Do("key", func() (int, error) {
		secondCalled = true
		return 2, nil
	})

	if !secondCalled {
		t.Fatal("expected a fresh fn invocation for the new caller")
	}
	if err != nil || v != 2 || shared {
		t.Errorf("Do = %v, %v, %v; want 2, nil, false", v, err, shared)
	}

	close(firstUnblock)
}

// A panic in fn is re-raised by every caller still attached when it
// occurs — mirrors TestPanicDo, but through DoContext.
func TestPanicDoContext(t *testing.T) {
	var g Group[string, int]
	fn := func(ctx context.Context) (int, error) {
		panic("invalid memory address or nil pointer dereference")
	}

	const n = 5
	waited := int32(n)
	panicCount := int32(0)
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer func() {
				if err := recover(); err != nil {
					atomic.AddInt32(&panicCount, 1)
				}
				if atomic.AddInt32(&waited, -1) == 0 {
					close(done)
				}
			}()
			g.DoContext(context.Background(), "key", fn) //nolint:errcheck
		}()
	}

	select {
	case <-done:
		if panicCount != n {
			t.Errorf("Expect %d panic, but got %d", n, panicCount)
		}
	case <-time.After(time.Second):
		t.Fatalf("DoContext hangs")
	}
}

// A panic that occurs after every caller has already timed out has nobody
// left to re-raise it: it must be absorbed rather than crash the process.
func TestDoContextPanicAbsorbedWhenAllCallersLeave(t *testing.T) {
	var g Group[string, int]

	fnPanicked := make(chan struct{})
	unblockFn := make(chan struct{})

	callerCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err, _ := g.DoContext(callerCtx, "key", func(ctx context.Context) (int, error) {
		<-unblockFn
		defer close(fnPanicked)
		panic("boom, nobody left to hear it")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}

	// Release fn only after the sole caller has already left.
	close(unblockFn)

	select {
	case <-fnPanicked:
	case <-time.After(time.Second):
		t.Fatal("fn never got to panic")
	}
	// If the panic had crashed the process (as it does for the plain,
	// non-context DoChan), this test binary would already be dead; reaching
	// this point proves it was absorbed instead.
}

// A panic delivered as a Result through DoChanContext must not report a
// Valid zero value: fn never got to produce one.
func TestPanicDoChanContextValueNotValid(t *testing.T) {
	var g Group[string, int]

	ch := g.DoChanContext(context.Background(), "key", func(ctx context.Context) (int, error) {
		panic("boom")
	})

	res := <-ch
	if res.Value.Valid {
		t.Errorf("a panicked call should not report a Valid zero value: %+v", res)
	}
	if res.Err == nil {
		t.Error("expected a non-nil Err")
	}
}

func TestGoexitDoContext(t *testing.T) {
	var g Group[string, *string]
	fn := func(ctx context.Context) (*string, error) {
		runtime.Goexit()
		return nil, nil
	}

	const n = 5
	waited := int32(n)
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			var err error
			defer func() {
				if err != nil {
					t.Errorf("Error should be nil, but got: %v", err)
				}
				if atomic.AddInt32(&waited, -1) == 0 {
					close(done)
				}
			}()
			_, err, _ = g.DoContext(context.Background(), "key", fn)
		}()
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("DoContext hangs")
	}
}

// A plain Do call joining a context-aware call never leaves early, so it
// must keep fn running past every context-aware caller's own timeout.
func TestDoContextInteropWithPlainDo(t *testing.T) {
	var g Group[string, int]

	fnStarted := make(chan struct{})
	unblockFn := make(chan struct{})
	var fnCtx context.Context

	leaderCtx, cancelLeader := context.WithCancel(context.Background())

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		g.DoContext(leaderCtx, "key", func(ctx context.Context) (int, error) { //nolint:errcheck
			fnCtx = ctx
			close(fnStarted)
			<-unblockFn
			return 42, nil
		})
	}()
	<-fnStarted

	doDone := make(chan struct{})
	var doValue int
	go func() {
		defer close(doDone)
		v, _, _ := g.Do("key", func() (int, error) {
			t.Error("fn should not be invoked again for a joining caller")
			return 0, nil
		})
		doValue = v
	}()

	// Wait until the plain Do has actually joined the in-flight call before
	// canceling the leader, so the cancellation always races against an
	// attached waiter instead of an empty map.
	for {
		g.mu.Lock()
		c, ok := g.m["key"]
		joined := ok && c.waiters == 2
		g.mu.Unlock()
		if joined {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancelLeader()
	<-leaderDone

	if fnCtx.Err() != nil {
		t.Errorf("fn's context should not be canceled while a plain Do is still attached")
	}

	close(unblockFn)
	<-doDone

	if doValue != 42 {
		t.Errorf("Do = %v; want 42", doValue)
	}
}

func TestDoContextAlreadyCanceled(t *testing.T) {
	var g Group[string, int]

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	v, err, shared := g.DoContext(ctx, "key", func(ctx context.Context) (int, error) {
		called = true
		return 1, nil
	})

	if called {
		t.Error("fn should not have been invoked")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if v != 0 || shared {
		t.Errorf("DoContext = %v, %v; want 0, false", v, shared)
	}
}

type contextTestKey struct{}

// fn must inherit the values of whichever caller started it, but never that
// caller's own cancellation — it keeps running for every other attached
// caller regardless of that one's fate.
func TestDoContextFnCtxInheritsValuesNotDeadline(t *testing.T) {
	var g Group[string, int]

	leaderCtx := context.WithValue(context.Background(), contextTestKey{}, "abc123")
	leaderCtx, cancelLeader := context.WithCancel(leaderCtx)

	fnStarted := make(chan struct{})
	unblockFn := make(chan struct{})
	var fnCtx context.Context

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		g.DoContext(leaderCtx, "key", func(ctx context.Context) (int, error) { //nolint:errcheck
			fnCtx = ctx
			close(fnStarted)
			<-unblockFn
			return 1, nil
		})
	}()
	<-fnStarted

	// A persistent joiner keeps fn's context alive past the leader's own
	// cancellation.
	joinerDone := make(chan struct{})
	go func() {
		defer close(joinerDone)
		g.Do("key", func() (int, error) { //nolint:errcheck
			t.Error("fn should not be invoked again for a joining caller")
			return 0, nil
		})
	}()

	for {
		g.mu.Lock()
		c, ok := g.m["key"]
		joined := ok && c.waiters == 2
		g.mu.Unlock()
		if joined {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancelLeader()
	<-leaderDone

	if fnCtx.Err() != nil {
		t.Errorf("fn's context should not inherit the leader's own cancellation, got err=%v", fnCtx.Err())
	}
	if v := fnCtx.Value(contextTestKey{}); v != "abc123" {
		t.Errorf("fn's context should inherit the leader's values, got %v", v)
	}

	close(unblockFn)
	<-joinerDone
}
