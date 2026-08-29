
# go-singleflightx

[![tag](https://img.shields.io/github/tag/samber/go-singleflightx.svg)](https://github.com/samber/go-singleflightx/releases)
![Go Version](https://img.shields.io/badge/Go-%3E%3D%201.21.0-%23007d9c)
[![GoDoc](https://godoc.org/github.com/samber/go-singleflightx?status.svg)](https://pkg.go.dev/github.com/samber/go-singleflightx)
![Build Status](https://github.com/samber/go-singleflightx/actions/workflows/test.yml/badge.svg)
[![Go report](https://goreportcard.com/badge/github.com/samber/go-singleflightx)](https://goreportcard.com/report/github.com/samber/go-singleflightx)
[![Coverage](https://img.shields.io/codecov/c/github/samber/go-singleflightx)](https://codecov.io/gh/samber/go-singleflightx)
[![Contributors](https://img.shields.io/github/contributors/samber/go-singleflightx)](https://github.com/samber/go-singleflightx/graphs/contributors)
[![License](https://img.shields.io/github/license/samber/go-singleflightx)](./LICENSE)

> x/sync/singleflight but better

## ✨ Features

This library is inspired by `x/sync/singleflight` but adds many features:
- 🧬 generics: type-safe API, no `interface{}` boxing overhead
- 🍱 batching: fetch multiple keys in a single callback, with in-flight deduplication
- 📭 nullable result
- 🍕 sharded groups
- ⏱️ per-caller timeout/cancellation, without interrupting other callers sharing the same call

### Performance

`x/sync/singleflight` uses `any` (aka `interface{}`) for return values, which means every value must be boxed into an interface on write and type-asserted on read. For value types (structs, ints, etc.), boxing triggers a heap allocation and increases GC pressure — an unnecessary cost in high-throughput or high-concurrency paths.

`go-singleflightx` uses **Go generics** so values are stored and returned as their concrete types, eliminating interface boxing and the associated allocations entirely.

## 🚀 Install

```sh
go get github.com/samber/go-singleflightx
```

This library is v0 and follows SemVer strictly. No breaking changes will be made to exported APIs before v1.0.0.

## 💡 Doc

GoDoc: [https://pkg.go.dev/github.com/samber/go-singleflightx](https://pkg.go.dev/github.com/samber/go-singleflightx)

## 🔥 Examples

Here is an example of a user retrieval in a caching layer:

```go
import "github.com/samber/go-singleflightx"

func getUsersByID(userIDs []string) (map[string]User, error) {
    users := []User{}

    // 📍 SQL query here...
    err := sqlx.Select(&users, "SELECT * FROM users WHERE id IN (?);", userIDs...)
    if err != nil {
        return nil, err
    }

    var results = map[string]User{}
    for _, u := range users {
        results[u.ID] = u
    }

    return results, nil
}

func main() {
    var g singleflightx.Group[string, User]

    // 👇 concurrent queries will be dedup
    output := g.DoX([]string{"user-1", "user-2"}, getUsersByID)
}
```

`output` is of type `map[K]singleflightx.Result[V]`, and will always have as many entries as requested, whatever the callback result.

```go
type Result[V any] struct {
  	 Value  singleflightx.NullValue[V]  // 💡 A result is considered "null" if the callback did not return it.
  	 Err    error
  	 Shared bool
}

type NullValue[V any] struct {
	Value V
	Valid bool
}
```

### Per-caller timeout, with `DoContext` / `DoXContext`

`DoContext`, `DoChanContext`, `DoXContext` and `DoChanXContext` accept a `context.Context` that bounds **only the calling goroutine's wait** — not the shared callback. If a caller's context is done before the callback returns, that caller gets the context's error immediately; every other caller attached to the same key keeps waiting for the real result, and the callback keeps running for them.

The callback's own context is only canceled once **every** caller of that key has stopped waiting for it (a plain, non-context `Do`/`DoX` caller never leaves early, so it always keeps the callback running for as long as it is attached).

```go
var g singleflightx.Group[string, User]

ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
defer cancel()

// If ctx expires, this call returns ctx.Err() right away, but the callback
// keeps running in case another caller is still waiting on "user-1".
user, err, shared := g.DoContext(ctx, "user-1", func(ctx context.Context) (User, error) {
    // 📍 SQL query here...
    var user User
    err := sqlx.GetContext(ctx, &user, "SELECT * FROM users WHERE id = ?;", "user-1")
    return user, err
})
```

```go
// Same bound, applied per key: a key already resolved keeps its real
// result even if ctx expires before the whole batch completes.
output := g.DoXContext(ctx, []string{"user-1", "user-2"}, func(ctx context.Context, userIDs []string) (map[string]User, error) {
    return getUsersByID(userIDs)
})
```

### Sharded groups, for high contention/concurrency environments

```go
g := singleflightx.NewShardedGroup[K string, User](10, func (key string) uint {
    h := fnv.New64a()
    h.Write([]byte(key))
    return uint(h.Sum64())
})

// as usual, but if the keys match different shards, getUsersByID will be called twice
output := g.DoX([]string{"user-1", "user-2"}, getUsersByID) 
```

### go-singleflightx + go-batchify

`go-batchify` groups concurrent tasks into a single batch. By adding `go-singleflightx`, you will be able to dedupe

```go
import (
    "golang.org/x/sync/singleflight"
    "github.com/samber/go-batchify"
)

var group singleflight.Group

batch := batchify.NewBatchWithTimer(
    10,
    func (ids []int) (map[int]string, error) {
        return ..., nil
    },
    5*time.Millisecond,
)

http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
    idStr := r.URL.Query().Get("id")
    id, _ := strconv.Atoi(idStr)

    value, err, _ = group.Do(idStr, func() (interface{}, error) {
        return batch.Do(id)
    })

    // ...
})
```

## 🤝 Contributing

- Ping me on Twitter [@samuelberthe](https://twitter.com/samuelberthe) (DMs, mentions, whatever :))
- Fork the [project](https://github.com/samber/go-singleflightx)
- Fix [open issues](https://github.com/samber/go-singleflightx/issues) or request new features

Don't hesitate ;)

```bash
# Install some dev dependencies
make tools

# Run tests
make test
# or
make watch-test
```

## 👤 Contributors

![Contributors](https://contrib.rocks/image?repo=samber/go-singleflightx)

## 💫 Show your support

Give a ⭐️ if this project helped you!

[![GitHub Sponsors](https://img.shields.io/github/sponsors/samber?style=for-the-badge)](https://github.com/sponsors/samber)

## 📝 License

Copyright © 2023 [Samuel Berthe](https://github.com/samber).

This project is [MIT](./LICENSE) licensed.
