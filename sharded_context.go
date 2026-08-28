package singleflightx

import "context"

// DoContext routes to the shard responsible for key. See Group.DoContext.
func (sg *ShardedGroup[K, V]) DoContext(ctx context.Context, key K, fn func(context.Context) (V, error)) (v V, err error, shared bool) {
	i := sg.hasher.computeHash(key, sg.count)
	return sg.shards[i].DoContext(ctx, key, fn)
}

// DoChanContext routes to the shard responsible for key. See Group.DoChanContext.
//
// The returned channel will not be closed.
func (sg *ShardedGroup[K, V]) DoChanContext(ctx context.Context, key K, fn func(context.Context) (V, error)) <-chan Result[V] {
	i := sg.hasher.computeHash(key, sg.count)
	return sg.shards[i].DoChanContext(ctx, key, fn)
}

// DoXContext routes keys to their shards, calling fn once per shard they
// span. See Group.DoXContext.
func (sg *ShardedGroup[K, V]) DoXContext(ctx context.Context, keys []K, fn func(context.Context, []K) (map[K]V, error)) map[K]Result[V] {
	ch := sg.DoChanXContext(ctx, keys, fn)

	results := make(map[K]Result[V], len(keys))
	for k, c := range ch {
		results[k] = <-c
	}

	return results
}

// DoChanXContext routes keys to their shards, calling fn once per shard they
// span. See Group.DoChanXContext.
//
// The returned channels will not be closed.
func (sg *ShardedGroup[K, V]) DoChanXContext(ctx context.Context, keys []K, fn func(context.Context, []K) (map[K]V, error)) map[K]chan Result[V] {
	keysByShard := partitionBy(keys, func(key K) uint {
		return sg.hasher.computeHash(key, sg.count)
	})

	results := make(map[K]chan Result[V], len(keys))
	for i, keys := range keysByShard {
		iter := sg.shards[i].DoChanXContext(ctx, keys, fn)
		for k, ch := range iter {
			results[k] = ch
		}
	}

	return results
}
