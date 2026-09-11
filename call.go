package singleflightx

import (
	"context"
	"runtime"
)

// callCtx is the cancelable context shared by every key resolved through one
// fn invocation. A batch call (DoXContext) resolves many keys through a
// single fn, so cancellation is refcounted per batch, not per key: it only
// fires once every caller of every key in the batch has stopped waiting.
type callCtx struct {
	ctx    context.Context
	cancel context.CancelCauseFunc

	// live counts callers, across every call sharing this callCtx, that are
	// still waiting for the result. Guarded by Group.mu.
	live int
}

// newCallCtx derives the cancelable context handed to fn from the context of
// whichever caller starts it: fn inherits that caller's values (useful for
// tracing/logging) but never its cancellation, since fn is shared and must
// keep running for every other caller attached to the same call regardless
// of this one's fate.
func newCallCtx(ctx context.Context) *callCtx {
	fnCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	return &callCtx{ctx: fnCtx, cancel: cancel}
}

// call is an in-flight or completed Do/DoX call.
type call[V any] struct {
	// done is closed under Group.mu once the call completes.
	done chan struct{}

	// These fields are written once before done is closed
	// and are only read after done is closed.
	value  V
	absent bool
	err    error

	// This field is read and written with the singleflight
	// mutex held before done is closed, and is read but
	// not written after done is closed.
	chans []chan<- Result[V]

	// cc is non-nil when this call was started by a XxxContext variant. It
	// is nil for calls started by the plain (non-context) variants, which
	// can never be interrupted early.
	cc *callCtx

	// waiters counts callers currently attached to this call, whether or
	// not they are themselves context-aware. A non-context caller never
	// decrements it early, which is what lets it keep a context-aware call
	// (and its shared callCtx) alive past every other caller's timeout: see
	// leave. Guarded by Group.mu.
	waiters int

	// hasPlainChan is true once a plain (non-context) DoChan/DoChanX caller
	// has ever registered a channel on this call, even if the call itself
	// was started by a XxxContext variant (cc != nil). A plain channel
	// caller has no per-caller way to re-raise a panic the way Do/DoX/
	// DoContext/DoXContext callers do via take() — it can only ever see
	// what doCall/doCallX push into its channel — so it alone decides
	// whether an unrecovered panic must still crash the process. Guarded
	// by Group.mu.
	hasPlainChan bool
}

// join registers one more caller on a call, mirroring what happens when the
// call is first created. Must be called with Group.mu held.
func (c *call[V]) join() {
	c.waiters++
	if c.cc != nil {
		c.cc.live++
	}
}

// leave detaches one caller from an in-flight call. If ch is non-nil, it is
// also removed from the call's notification list so the eventual teardown
// doesn't push a second result into an already-abandoned, capacity-1
// channel. Once every caller has left, the call's key is removed from the
// group's map — so the next caller starts a fresh call instead of joining
// one nobody wants anymore — and, if the call is context-aware, fn's context
// is canceled once every caller of the whole batch has left.
//
// ok is false when the call already completed — checked under Group.mu, the
// same lock doCall/doCallX hold while closing c.done — in which case the
// caller must use the real result instead of treating this as an early exit.
// waiters (only meaningful when ok is true) is the count of callers still
// attached after this one has detached, i.e. it already excludes the
// caller that just left.
func (g *Group[K, V]) leave(key K, c *call[V], ch chan Result[V]) (ok bool, waiters int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	select {
	case <-c.done:
		return false, 0
	default:
	}

	if ch != nil {
		for i, existing := range c.chans {
			if existing == ch {
				c.chans = append(c.chans[:i], c.chans[i+1:]...)
				break
			}
		}
	}

	c.waiters--
	if c.waiters == 0 && g.m[key] == c {
		delete(g.m, key)
	}

	if c.cc != nil {
		c.cc.live--
		if c.cc.live == 0 {
			c.cc.cancel(nil)
		}
	}

	return true, c.waiters
}

// take reads the result of a completed call, re-raising a panic or
// runtime.Goexit exactly as fn triggered it, once per blocking caller.
//
// Unlike leave, take doesn't detach the calling caller from c, so waiters
// still counts it: shared is waiters > 1, i.e. "is anyone *else* still
// attached", not just waiters > 0.
func (c *call[V]) take() (v V, err error, shared bool) {
	if e, ok := c.err.(*panicError); ok {
		panic(e)
	} else if c.err == errGoexit {
		runtime.Goexit()
	}
	return c.value, c.err, c.waiters > 1
}

// takeResult is like take but shaped for the batch (X) variants, which
// report results as Result[V] rather than a bare (value, err, shared) triple.
func (c *call[V]) takeResult() Result[V] {
	v, err, shared := c.take()
	return Result[V]{NullValue[V]{v, !c.absent}, err, shared}
}
