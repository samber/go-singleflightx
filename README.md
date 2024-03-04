
# go-singleflightx

[![tag](https://img.shields.io/github/tag/samber/go-singleflightx.svg)](https://github.com/samber/go-singleflightx/releases)
![Go Version](https://img.shields.io/badge/Go-%3E%3D%201.18.0-%23007d9c)
[![GoDoc](https://godoc.org/github.com/samber/go-singleflightx?status.svg)](https://pkg.go.dev/github.com/samber/go-singleflightx)
![Build Status](https://github.com/samber/go-singleflightx/actions/workflows/test.yml/badge.svg)
[![Go report](https://goreportcard.com/badge/github.com/samber/go-singleflightx)](https://goreportcard.com/report/github.com/samber/go-singleflightx)
[![Coverage](https://img.shields.io/codecov/c/github/samber/go-singleflightx)](https://codecov.io/gh/samber/go-singleflightx)
[![Contributors](https://img.shields.io/github/contributors/samber/go-singleflightx)](https://github.com/samber/go-singleflightx/graphs/contributors)
[![License](https://img.shields.io/github/license/samber/go-singleflightx)](./LICENSE)

> x/sync/singleflight but better

## Features

This library is inspired by `x/sync/singleflight` but adds many features:
- 🧬 generics
- 🍱 batching: fetch multiple keys in a single callback, with in-flight deduplication
- 📭 nullable result

## 🚀 Install

```sh
go get github.com/samber/go-singleflightx
```

This library is v0 and follows SemVer strictly. No breaking changes will be made to exported APIs before v1.0.0.

## 💡 Doc

GoDoc: [https://pkg.go.dev/github.com/samber/go-singleflightx](https://pkg.go.dev/github.com/samber/go-singleflightx)

## Examples

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

`output` is of type `map[K]singleflightx.Result[V]`, and will have always as many entries as requested, whatever the result of callback.

```go
type Result[V any] struct {
  	 Value  singleflightx.NullValue[V]  // 💡 A result is considered as "null" if the callback did not return it.
  	 Err    error
  	 Shared bool
}

type NullValue[V any] struct {
	Value V
	Valid bool
}
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
