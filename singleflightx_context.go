package singleflightx

import "context"

// DoXContext is like DoX, but ctx bounds the wait for every key in this
// call. Once ctx is done, keys still in flight get ctx's error immediately;
// keys that already completed keep their real result. See DoContext for how
// fn's own cancellation is derived — for a batch, fn shares one context
// across every key it is asked to resolve, canceled only once every caller
// of every key in the batch has stopped waiting.
func (g *Group[K, V]) DoXContext(ctx context.Context, keys []K, fn func(context.Context, []K) (map[K]V, error)) map[K]Result[V] {
	results := make(map[K]Result[V], len(keys))

	if ctx.Err() != nil {
		cause := context.Cause(ctx)
		for _, k := range keys {
			results[k] = Result[V]{Err: cause}
		}
		return results
	}

	calls, toCall := g.startOrJoinContextX(ctx, keys, nil)
	if len(toCall) > 0 {
		cc := calls[toCall[0]].cc
		go g.doCallX(calls, toCall, func(keys []K) (map[K]V, error) { return fn(cc.ctx, keys) })
	}

	// Wait on every key before extracting any of them: take() (called
	// below) can panic or call runtime.Goexit() for a key whose fn failed
	// that way, which would otherwise unwind this loop before later keys —
	// possibly sharing an unrelated, still in-flight call — got their
	// waitResult/leave() call. See waitResult's doc comment.
	toExtract := make(map[K]*call[V], len(calls))
	for k, c := range calls {
		if left, res := g.waitResult(ctx, k, c); left {
			results[k] = res
		} else {
			toExtract[k] = c
		}
	}
	for k, c := range toExtract {
		results[k] = c.takeResult()
	}

	return results
}

// startOrJoinContextX looks up or starts calls for every key in keys, backed
// by one shared callCtx for the newly started ones. chans, when non-nil,
// must have one entry per key and is registered on each call immediately,
// whether joining or creating — shared by DoXContext (chans == nil) and
// DoChanXContext.
func (g *Group[K, V]) startOrJoinContextX(ctx context.Context, keys []K, chans map[K]chan Result[V]) (calls map[K]*call[V], toCall []K) {
	keys = uniqKeys(keys)
	calls = make(map[K]*call[V], len(keys))

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.m == nil {
		g.m = make(map[K]*call[V])
	}
	for _, k := range keys {
		if c, ok := g.m[k]; ok {
			c.join()
			if chans != nil {
				c.chans = append(c.chans, chans[k])
			}
			calls[k] = c
		} else {
			var callChans []chan<- Result[V]
			if chans != nil {
				callChans = []chan<- Result[V]{chans[k]}
			}
			c := &call[V]{done: make(chan struct{}), chans: callChans}
			g.m[k] = c
			calls[k] = c
			toCall = append(toCall, k)
		}
	}

	if len(toCall) > 0 {
		cc := newCallCtx(ctx)
		for _, k := range toCall {
			calls[k].cc = cc
			calls[k].join()
		}
	}

	return calls, toCall
}

// DoChanXContext is like DoChanX, but ctx bounds the wait for every key in
// this call: once ctx is done, the channels for keys still in flight receive
// ctx's error instead of fn's result. See DoXContext for how fn's own
// cancellation is derived.
//
// The returned channels will not be closed.
func (g *Group[K, V]) DoChanXContext(ctx context.Context, keys []K, fn func(context.Context, []K) (map[K]V, error)) map[K]chan Result[V] {
	// Dedupe before allocating channels: a repeated key must map to exactly
	// one capacity-1 channel, or the ctx-already-done fast path below would
	// send into the same channel more than once and block forever on the
	// second send (see startOrJoinContextX, which needs the same dedup for
	// the non-fast-path case).
	keys = uniqKeys(keys)
	results := make(map[K]chan Result[V], len(keys))
	for _, k := range keys {
		results[k] = make(chan Result[V], 1)
	}

	if ctx.Err() != nil {
		cause := context.Cause(ctx)
		for _, k := range keys {
			results[k] <- Result[V]{Err: cause}
		}
		return results
	}

	calls, toCall := g.startOrJoinContextX(ctx, keys, results)
	if len(toCall) > 0 {
		cc := calls[toCall[0]].cc
		go g.doCallX(calls, toCall, func(keys []K) (map[K]V, error) { return fn(cc.ctx, keys) })
	}

	for k, c := range calls {
		go g.watchContext(ctx, k, c, results[k])
	}

	return results
}
