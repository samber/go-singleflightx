package singleflightx

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
