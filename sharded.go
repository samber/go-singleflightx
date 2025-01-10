package singleflightx

func NewShardedGroup[K comparable, V any](count uint, hasher Hasher[K]) *ShardedGroup[K, V] {
	shards := make([]Group[K, V], count)
	for i := range shards {
		shards[i] = Group[K, V]{}
	}
	return &ShardedGroup[K, V]{count: count, shards: shards, hasher: hasher}
}

// ShardedGroup is a duplicate of singleflight.Group, but with the ability to shard the map of calls.
type ShardedGroup[K comparable, V any] struct {
	count  uint
	shards []Group[K, V]
	hasher Hasher[K]
}

// Do executes and returns the results of the given function, making
// sure that only one execution is in-flight for a given key at a
// time. If a duplicate comes in, the duplicate caller waits for the
// original to complete and receives the same results.
// The return value shared indicates whether v was given to multiple callers.
// Even if fn does not return V on some keys, the results map will contain
// those keys with a `Valid` field set to false.
func (sg *ShardedGroup[K, V]) Do(key K, fn func() (V, error)) (v V, err error, shared bool) {
	i := sg.hasher.computeHash(key, sg.count)
	return sg.shards[i].Do(key, fn)
}

// DoChan is like Do but returns a channel that will receive the
// results when they are ready.
//
// The returned channel will not be closed.
func (sg *ShardedGroup[K, V]) DoChan(key K, fn func() (V, error)) <-chan Result[V] {
	i := sg.hasher.computeHash(key, sg.count)
	return sg.shards[i].DoChan(key, fn)
}

// DoX executes and returns the results of the given function, making
// sure that only one execution is in-flight for a given key at a
// time. If a duplicate comes in, the duplicate caller waits for the
// original to complete and receives the same results.
// The return value shared indicates whether v was given to multiple callers.
func (sg *ShardedGroup[K, V]) DoX(keys []K, fn func([]K) (map[K]V, error)) (results map[K]Result[V]) {
	ch := sg.DoChanX(keys, fn)

	results = make(map[K]Result[V], len(keys))
	for k, c := range ch {
		results[k] = <-c
	}

	return results
}

// DoChanX is like Do but returns a channel that will receive the
// results when they are ready.
//
// The returned channel will not be closed.
func (sg *ShardedGroup[K, V]) DoChanX(keys []K, fn func([]K) (map[K]V, error)) map[K]chan Result[V] {
	keysByShard := partitionBy(keys, func(key K) uint {
		return sg.hasher.computeHash(key, sg.count)
	})

	results := make(map[K]chan Result[V], len(keys))
	for i, keys := range keysByShard {
		iter := sg.shards[i].DoChanX(keys, fn)
		for k, ch := range iter {
			results[k] = ch
		}
	}

	return results
}

// Forget tells the singleflight to forget about a key.  Future calls
// to Do for this key will call the function rather than waiting for
// an earlier call to complete.
func (sg *ShardedGroup[K, V]) Forget(key K) {
	i := sg.hasher.computeHash(key, sg.count)
	sg.shards[i].Forget(key)
}

// ForgetX tells the singleflight to forget about many keys.  Future calls
// to Do for this key will call the function rather than waiting for
// an earlier call to complete.
func (sg *ShardedGroup[K, V]) ForgetX(keys []K) {
	keysByShard := partitionBy(keys, func(key K) uint {
		return sg.hasher.computeHash(key, sg.count)
	})

	for i, keys := range keysByShard {
		sg.shards[i].ForgetX(keys)
	}
}
