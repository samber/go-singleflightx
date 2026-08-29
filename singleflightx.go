package singleflightx

import "runtime"

// DoX executes and returns the results of the given function, making
// sure that only one execution is in-flight for a given key at a
// time. If a duplicate comes in, the duplicate caller waits for the
// original to complete and receives the same results.
// The return value shared indicates whether v was given to multiple callers.
// Even if fn does not return V on some keys, the results map will contain
// those keys with a `Valid` field set to false.
func (g *Group[K, V]) DoX(keys []K, fn func([]K) (map[K]V, error)) (results map[K]Result[V]) {
	keys = uniqKeys(keys)
	results = make(map[K]Result[V], len(keys))
	calls := make(map[K]*call[V], len(keys))
	toCall := []K{}

	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[K]*call[V])
	}
	for _, k := range keys {
		if c, ok := g.m[k]; ok {
			c.dups++
			c.join()
			calls[k] = c
		} else {
			c := &call[V]{done: make(chan struct{})}
			c.join()
			g.m[k] = c
			calls[k] = c
			toCall = append(toCall, k)
		}
	}
	g.mu.Unlock()

	g.doCallX(calls, toCall, fn)

	for k, c := range calls {
		<-c.done

		if e, ok := c.err.(*panicError); ok {
			panic(e)
		} else if c.err == errGoexit {
			runtime.Goexit()
		}

		results[k] = Result[V]{NullValue[V]{c.value, !c.absent}, c.err, c.dups > 0}
	}

	return results
}

// DoChanX is like Do but returns a channel that will receive the
// results when they are ready.
//
// The returned channel will not be closed.
func (g *Group[K, V]) DoChanX(keys []K, fn func([]K) (map[K]V, error)) map[K]chan Result[V] {
	keys = uniqKeys(keys)
	results := make(map[K]chan Result[V], len(keys))
	for _, k := range keys {
		results[k] = make(chan Result[V], 1)
	}

	calls := make(map[K]*call[V], len(keys))
	toCall := []K{}

	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[K]*call[V])
	}
	for _, k := range keys {
		if c, ok := g.m[k]; ok {
			c.dups++
			c.join()
			c.chans = append(g.m[k].chans, results[k])
			calls[k] = c
		} else {
			c := &call[V]{done: make(chan struct{}), chans: []chan<- Result[V]{results[k]}}
			c.join()
			g.m[k] = c
			calls[k] = c
			toCall = append(toCall, k)
		}
	}
	g.mu.Unlock()

	go g.doCallX(calls, toCall, fn)

	return results
}

// doCallX handles the single call for a key.
func (g *Group[K, V]) doCallX(c map[K]*call[V], keys []K, fn func([]K) (map[K]V, error)) {
	if len(keys) == 0 {
		return
	}

	normalReturn := false
	recovered := false

	// use double-defer to distinguish panic from runtime.Goexit,
	// more details see https://golang.org/cl/134395
	defer func() {
		// the given function invoked runtime.Goexit
		if !normalReturn && !recovered {
			for _, key := range keys {
				c[key].err = errGoexit
			}
		}

		g.mu.Lock()
		defer g.mu.Unlock()

		for _, key := range keys {
			close(c[key].done)
			if g.m[key] == c[key] {
				delete(g.m, key)
			}
		}

		// Every key in this batch shares the same callCtx (or none), created
		// once by the caller before doCallX was launched.
		cc := c[keys[0]].cc
		if cc != nil {
			cc.cancel(nil)
		}

		for _, key := range keys {
			if e, ok := c[key].err.(*panicError); ok && cc == nil {
				// In order to prevent the waiting channels from being blocked forever,
				// needs to ensure that this panic cannot be recovered.
				if len(c[key].chans) > 0 {
					go panic(e)
					select {} // Keep this goroutine around so that it will appear in the crash dump.
				} else {
					panic(e)
				}
			} else if c[key].err == errGoexit {
				// Already in the process of goexit, no need to call again
			} else {
				// Normal return, or a context-aware call's panic (cc != nil):
				// delivered as a plain Result instead of crashing the process, so
				// DoXContext callers can re-panic themselves and DoChanXContext
				// callers see it in Result.Err.
				for _, ch := range c[key].chans {
					ch <- Result[V]{NullValue[V]{c[key].value, !c[key].absent}, c[key].err, c[key].dups > 0}
				}
			}
		}
	}()

	func() {
		defer func() {
			if !normalReturn {
				// Ideally, we would wait to take a stack trace until we've determined
				// whether this is a panic or a runtime.Goexit.
				//
				// Unfortunately, the only way we can distinguish the two is to see
				// whether the recover stopped the goroutine from terminating, and by
				// the time we know that, the part of the stack trace relevant to the
				// panic has been discarded.
				if r := recover(); r != nil {
					// A context-aware batch delivers this as a Result instead of
					// crashing the process (see the finalize defer below), so
					// absent must be set here too, or Valid would wrongly report
					// true for the zero value fn never got to produce.
					for _, key := range keys {
						c[key].err = newPanicError(r)
						c[key].absent = true
					}
				}
			}
		}()

		values, err := fn(keys)
		if values == nil {
			values = make(map[K]V, len(keys))
		}

		for _, key := range keys {
			c[key].err = err
			if v, ok := values[key]; ok {
				c[key].value = v
			} else {
				c[key].absent = true
			}
		}

		normalReturn = true
	}()

	if !normalReturn {
		recovered = true
	}
}

// ForgetX tells the singleflight to forget about many keys.  Future calls
// to Do for this key will call the function rather than waiting for
// an earlier call to complete.
func (g *Group[K, V]) ForgetX(keys []K) {
	g.mu.Lock()
	for _, key := range keys {
		delete(g.m, key)
	}
	g.mu.Unlock()
}
