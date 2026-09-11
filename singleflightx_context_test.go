package singleflightx

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoXContext(t *testing.T) {
	var g Group[string, string]

	v := g.DoXContext(context.Background(), []string{"key"}, func(ctx context.Context, keys []string) (map[string]string, error) {
		return map[string]string{"key": "bar"}, nil
	})
	if v["key"].Value.Value != "bar" || v["key"].Err != nil || !v["key"].Value.Valid {
		t.Errorf("unexpected result: %+v", v["key"])
	}

	v = g.DoXContext(context.Background(), []string{"a", "b"}, func(ctx context.Context, keys []string) (map[string]string, error) {
		return map[string]string{"a": "foo", "b": "bar"}, nil
	})
	if v["a"].Value.Value != "foo" || v["b"].Value.Value != "bar" {
		t.Errorf("unexpected results: %+v", v)
	}

	// partial result: a key fn didn't return comes back invalid, not an error.
	v = g.DoXContext(context.Background(), []string{"a", "b"}, func(ctx context.Context, keys []string) (map[string]string, error) {
		return map[string]string{"a": "foo"}, nil
	})
	if !v["a"].Value.Valid || v["b"].Value.Valid {
		t.Errorf("unexpected validity: a=%v b=%v", v["a"].Value.Valid, v["b"].Value.Valid)
	}
}

func TestDoXContextErr(t *testing.T) {
	var g Group[string, string]
	someErr := errors.New("some error")

	v := g.DoXContext(context.Background(), []string{"a", "b"}, func(ctx context.Context, keys []string) (map[string]string, error) {
		return nil, someErr
	})
	if v["a"].Err != someErr || v["b"].Err != someErr {
		t.Errorf("unexpected errors: %v, %v", v["a"].Err, v["b"].Err)
	}
	if v["a"].Value.Valid || v["b"].Value.Valid {
		t.Errorf("values should not be valid on error")
	}
}

func TestDoChanXContext(t *testing.T) {
	var g Group[string, string]

	chans := g.DoChanXContext(context.Background(), []string{"a", "b"}, func(ctx context.Context, keys []string) (map[string]string, error) {
		return map[string]string{"a": "foo", "b": "bar"}, nil
	})

	res1 := <-chans["a"]
	res2 := <-chans["b"]

	if res1.Value.Value != "foo" || res2.Value.Value != "bar" {
		t.Errorf("unexpected results: %+v, %+v", res1, res2)
	}
	if res1.Err != nil || res2.Err != nil {
		t.Errorf("unexpected errors: %v, %v", res1.Err, res2.Err)
	}
}

// A key repeated 3+ times in one DoChanXContext call must still register
// its capacity-1 result channel exactly once: a second send into an
// already-full channel would block forever while Group.mu is held, wedging
// every other call on the Group.
func TestDoChanXContextDuplicateKeyDoesNotDeadlockGroup(t *testing.T) {
	var g Group[string, int]

	chans := g.DoChanXContext(context.Background(), []string{"a", "a", "a"}, func(ctx context.Context, keys []string) (map[string]int, error) {
		return map[string]int{"a": 1}, nil
	})
	if len(chans) != 1 {
		t.Fatalf("len(chans) = %d; want 1", len(chans))
	}

	res := <-chans["a"]
	if res.Value.Value != 1 || !res.Value.Valid {
		t.Errorf("unexpected result: %+v", res)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Do("unrelated", func() (int, error) { return 2, nil }) //nolint:errcheck
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Group.mu is wedged: an unrelated Do never returned")
	}
}

// The already-canceled-ctx fast path builds one channel per key up front
// and sends into it without ever going through startOrJoinContextX, so it
// needs its own dedup: a repeated key must still map to exactly one
// capacity-1 channel, or the second send blocks forever with nothing left
// to drain it.
func TestDoChanXContextDuplicateKeyAlreadyCanceledDoesNotHang(t *testing.T) {
	var g Group[string, int]

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	var chans map[string]chan Result[int]
	go func() {
		defer close(done)
		chans = g.DoChanXContext(ctx, []string{"a", "a", "a"}, func(ctx context.Context, keys []string) (map[string]int, error) {
			t.Error("fn should not have been invoked")
			return nil, nil
		})
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DoChanXContext hangs on a duplicate key with an already-canceled ctx")
	}

	if len(chans) != 1 {
		t.Fatalf("len(chans) = %d; want 1", len(chans))
	}
	res := <-chans["a"]
	if !errors.Is(res.Err, context.Canceled) {
		t.Errorf("chans[%q].Err = %v; want context.Canceled", "a", res.Err)
	}
}

// A panic delivered as a Result through DoChanXContext must not report a
// Valid zero value: fn never got to produce one.
func TestPanicDoChanXContextValueNotValid(t *testing.T) {
	var g Group[string, int]

	chans := g.DoChanXContext(context.Background(), []string{"key"}, func(ctx context.Context, keys []string) (map[string]int, error) {
		panic("boom")
	})

	res := <-chans["key"]
	if res.Value.Valid {
		t.Errorf("a panicked call should not report a Valid zero value: %+v", res)
	}
	if res.Err == nil {
		t.Error("expected a non-nil Err")
	}
}

func TestDoXContextAlreadyCanceled(t *testing.T) {
	var g Group[string, int]

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	results := g.DoXContext(ctx, []string{"a", "b"}, func(ctx context.Context, keys []string) (map[string]int, error) {
		called = true
		return nil, nil
	})

	if called {
		t.Error("fn should not have been invoked")
	}
	for _, k := range []string{"a", "b"} {
		if !errors.Is(results[k].Err, context.Canceled) {
			t.Errorf("results[%q].Err = %v; want context.Canceled", k, results[k].Err)
		}
		if results[k].Value.Valid {
			t.Errorf("results[%q] should not carry a value", k)
		}
	}
}

// A key already resolved before ctx expires keeps its real result; a key
// still in flight when ctx expires gets ctx's error instead.
func TestDoXContextPartialTimeout(t *testing.T) {
	var g Group[string, int]

	// "a" is started by a plain, separate DoX call and stays in flight
	// until the test lets it resolve.
	aStarted := make(chan struct{})
	aUnblock := make(chan struct{})
	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		g.DoX([]string{"a"}, func(keys []string) (map[string]int, error) { //nolint:errcheck
			close(aStarted)
			<-aUnblock
			return map[string]int{"a": 1}, nil
		})
	}()
	<-aStarted // "a" is guaranteed present in g.m before DoXContext runs

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	bUnblock := make(chan struct{})
	defer close(bUnblock) // release the still-running fn once the test ends

	// Resolve "a" shortly after DoXContext starts waiting on it, well
	// before ctx's deadline; "b" (brand new) stays blocked past it.
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(aUnblock)
	}()

	results := g.DoXContext(ctx, []string{"a", "b"}, func(ctx context.Context, keys []string) (map[string]int, error) {
		<-bUnblock
		return map[string]int{"b": 2}, nil
	})

	if results["a"].Err != nil || !results["a"].Value.Valid || results["a"].Value.Value != 1 {
		t.Errorf(`results["a"] = %+v; want the real value, resolved before ctx expired`, results["a"])
	}
	if !errors.Is(results["b"].Err, context.DeadlineExceeded) {
		t.Errorf(`results["b"].Err = %v; want DeadlineExceeded`, results["b"].Err)
	}
	if results["b"].Value.Valid {
		t.Errorf(`results["b"] should not carry a value once ctx expired`)
	}

	<-aDone
}

// A panic in fn is re-raised by every caller still attached when it occurs.
func TestPanicDoXContext(t *testing.T) {
	var g Group[string, int]
	fn := func(ctx context.Context, keys []string) (map[string]int, error) {
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
			g.DoXContext(context.Background(), []string{"key"}, fn)
		}()
	}

	select {
	case <-done:
		if panicCount != n {
			t.Errorf("Expect %d panic, but got %d", n, panicCount)
		}
	case <-time.After(time.Second):
		t.Fatalf("DoXContext hangs")
	}
}

func TestGoexitDoXContext(t *testing.T) {
	var g Group[string, *string]
	fn := func(ctx context.Context, keys []string) (map[string]*string, error) {
		runtime.Goexit()
		return nil, nil
	}

	const n = 5
	waited := int32(n)
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer func() {
				if atomic.AddInt32(&waited, -1) == 0 {
					close(done)
				}
			}()
			_ = g.DoXContext(context.Background(), []string{"key"}, fn)
		}()
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("DoXContext hangs")
	}
}

func TestShardedGroupDoXContext(t *testing.T) {
	hasher := Hasher[string](func(key string) uint64 {
		var h uint64
		for _, b := range []byte(key) {
			h = h*31 + uint64(b)
		}
		return h
	})
	sg := NewShardedGroup[string, int](4, hasher)

	results := sg.DoXContext(context.Background(), []string{"a", "b", "c", "d"}, func(ctx context.Context, keys []string) (map[string]int, error) {
		out := make(map[string]int, len(keys))
		for _, k := range keys {
			out[k] = len(k)
		}
		return out, nil
	})

	for _, k := range []string{"a", "b", "c", "d"} {
		if results[k].Err != nil || !results[k].Value.Valid {
			t.Errorf("results[%q] = %+v; want a resolved value", k, results[k])
		}
	}
}

func TestShardedGroupDoContext(t *testing.T) {
	hasher := Hasher[string](func(key string) uint64 {
		var h uint64
		for _, b := range []byte(key) {
			h = h*31 + uint64(b)
		}
		return h
	})
	sg := NewShardedGroup[string, string](4, hasher)

	v, err, _ := sg.DoContext(context.Background(), "key", func(ctx context.Context) (string, error) {
		return "bar", nil
	})
	if v != "bar" || err != nil {
		t.Errorf("DoContext = %v, %v; want bar, nil", v, err)
	}

	ch := sg.DoChanContext(context.Background(), "key", func(ctx context.Context) (string, error) {
		return "baz", nil
	})
	res := <-ch
	if res.Value.Value != "baz" || res.Err != nil {
		t.Errorf("DoChanContext result = %+v; want baz, nil", res)
	}
}

// A panic on one key of a batch must not skip leave() on another key that
// happens to share the same iteration, even though extracting the panicking
// key's result (via take()) unwinds the caller's goroutine. Otherwise an
// unrelated call sharing this batch's calls map would have its join() left
// permanently unbalanced, leaking a waiter it can never free.
func TestDoXContextPanicOnOneKeyStillLeavesOthers(t *testing.T) {
	var g Group[string, int]

	// "stuck" is a separate, unrelated call that outlives this whole test
	// until explicitly unblocked at the end.
	stuckStarted := make(chan struct{})
	stuckUnblock := make(chan struct{})
	stuckDone := make(chan struct{})
	go func() {
		defer close(stuckDone)
		g.DoContext(context.Background(), "stuck", func(ctx context.Context) (int, error) { //nolint:errcheck
			close(stuckStarted)
			<-stuckUnblock
			return 1, nil
		})
	}()
	<-stuckStarted

	// The batch's own ctx expires well before "stuck" ever resolves, so
	// waitResult's ctx.Done() branch is what detaches this batch from
	// "stuck" -- not a normal completion.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected DoXContext to re-panic for the \"panicky\" key")
			}
		}()
		g.DoXContext(ctx, []string{"stuck", "panicky"}, func(ctx context.Context, keys []string) (map[string]int, error) {
			panic("boom")
		})
		t.Error("DoXContext should not return normally")
	}()

	g.mu.Lock()
	c, ok := g.m["stuck"]
	waiters := -1
	if ok {
		waiters = c.waiters
	}
	g.mu.Unlock()
	if !ok || waiters != 1 {
		t.Errorf(`g.m["stuck"].waiters = %d (present=%v); want 1 (only the original caller left attached)`, waiters, ok)
	}

	close(stuckUnblock)
	<-stuckDone
}
