package singleflightx

import "context"

// DoContext is like Do, but the wait is bounded by ctx: if ctx is done
// before fn returns, DoContext returns immediately with ctx's error and
// shared reports whether any other caller was also attached to this key at
// that moment.
//
// fn itself is not necessarily interrupted: it keeps running for every other
// caller still attached to the same key. It only receives a canceled context
// once every caller of this key — across every variant, context-aware or
// not — has stopped waiting. That context carries the values (but never the
// cancellation) of whichever caller happened to start it, since fn is shared
// and must not depend on a single caller's deadline or lifetime.
//
// A plain Do call joining the same key never leaves early, so it prevents fn
// from ever being interrupted while it is attached.
func (g *Group[K, V]) DoContext(ctx context.Context, key K, fn func(context.Context) (V, error)) (v V, err error, shared bool) {
	if ctx.Err() != nil {
		return v, context.Cause(ctx), false
	}

	c, started := g.startOrJoinContext(ctx, key, nil)
	if started {
		go g.doCall(c, key, func() (V, error) { return fn(c.cc.ctx) })
	}

	r := g.awaitResult(ctx, key, c)
	return r.Value.Value, r.Err, r.Shared
}

// startOrJoinContext looks up an in-flight call for key, or starts a new one
// backed by a fresh callCtx. ch, when non-nil, is registered on the call
// immediately, whether joining or creating — shared by DoContext (ch == nil)
// and DoChanContext.
func (g *Group[K, V]) startOrJoinContext(ctx context.Context, key K, ch chan Result[V]) (c *call[V], started bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.m == nil {
		g.m = make(map[K]*call[V])
	}
	if c, ok := g.m[key]; ok {
		c.dups++
		c.join()
		if ch != nil {
			c.chans = append(c.chans, ch)
		}
		return c, false
	}

	var chans []chan<- Result[V]
	if ch != nil {
		chans = []chan<- Result[V]{ch}
	}
	c = &call[V]{done: make(chan struct{}), cc: newCallCtx(ctx), chans: chans}
	c.join()
	g.m[key] = c
	return c, true
}

// awaitResult waits for c to complete or ctx to expire, whichever comes
// first, detaching the caller from c in the latter case. Shared by the
// single-key and batch (X) blocking variants.
func (g *Group[K, V]) awaitResult(ctx context.Context, key K, c *call[V]) Result[V] {
	select {
	case <-c.done:
		return c.takeResult()
	case <-ctx.Done():
		if ok, dups := g.leave(key, c, nil); ok {
			return Result[V]{Err: context.Cause(ctx), Shared: dups > 0}
		}
		// The call completed in the race between both select cases: use the
		// real result instead of ctx's error.
		return c.takeResult()
	}
}

// DoChanContext is like DoChan, but ctx bounds only this caller's channel:
// if ctx is done before fn returns, the returned channel receives ctx's
// error instead of fn's result. See DoContext for how fn's own cancellation
// is derived.
//
// The returned channel will not be closed.
func (g *Group[K, V]) DoChanContext(ctx context.Context, key K, fn func(context.Context) (V, error)) <-chan Result[V] {
	ch := make(chan Result[V], 1)

	if ctx.Err() != nil {
		ch <- Result[V]{Err: context.Cause(ctx)}
		return ch
	}

	c, started := g.startOrJoinContext(ctx, key, ch)
	if started {
		go g.doCall(c, key, func() (V, error) { return fn(c.cc.ctx) })
	}
	go g.watchContext(ctx, key, c, ch)

	return ch
}

// watchContext delivers ctx's error to ch if ctx expires before c completes,
// detaching the caller from c so the eventual teardown does not also push a
// second result into ch.
func (g *Group[K, V]) watchContext(ctx context.Context, key K, c *call[V], ch chan Result[V]) {
	select {
	case <-c.done:
		// The real result was already, or will be, pushed to ch by doCall's
		// teardown, since we never left the call.
	case <-ctx.Done():
		if ok, dups := g.leave(key, c, ch); ok {
			ch <- Result[V]{Err: context.Cause(ctx), Shared: dups > 0}
		}
	}
}
