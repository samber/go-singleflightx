package singleflightx

// uniqKeys returns keys with duplicates removed, keeping each key's first
// occurrence. A key repeated within one DoX/DoChanX/DoXContext/DoChanXContext
// call would otherwise be joined to its own freshly created call a second
// time in the same critical section, registering the same result channel
// more than once in call.chans: doCallX then sends into that capacity-1
// channel twice while holding Group.mu, and the second send blocks forever
// once nothing is left to drain it, wedging the whole Group.
func uniqKeys[K comparable](keys []K) []K {
	seen := make(map[K]struct{}, len(keys))
	out := make([]K, 0, len(keys))
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

func partitionBy[K comparable](collection []K, iteratee func(item K) uint) map[uint][]K {
	result := map[uint][]K{}

	for _, item := range collection {
		key := iteratee(item)

		_, ok := result[key]
		if !ok {
			result[key] = []K{}
		}

		result[key] = append(result[key], item)
	}

	return result
}
