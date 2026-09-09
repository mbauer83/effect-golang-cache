# effect-golang-cache

Adapters for the two ports in
[`effect-golang`](https://github.com/mbauer83/effect-golang) that a program
reaches for when it reads something expensive: `cache.Store`, where it keeps
what it has already found out, and `rate.Limiter`, where it keeps the count of
how often it has asked somebody else.

The ports and the in-process adapters are in the runtime, and need nothing.
This module is where the ones that need a client library live -- one package
per backend, starting with Redis -- because a module is the unit of dependency
and a client belongs in a module that exists to be an adapter. That is the same
line `effect-golang-sql` draws by depending on a port and leaving the driver to
the application, and `effect-golang-schema` and `effect-golang-web` draw by
naming their third parties only in tests.

```go
effect.Scoped(func(scope effect.Scope) effect.Effect[R, redis.Fault, A] {
    return redis.Reaching[R](scope, &goredis.Options{Addr: "localhost:6379"}).
        FlatMap(func(client *goredis.Client) effect.Effect[R, redis.Fault, A] {
            keeping := redis.Keeping(client) // cache.Store
            pacing := redis.Pacing(client)   // rate.Limiter
            ...
        })
})
```

Both are shared on purpose, and that is the whole reason to reach for these
rather than the in-process adapters in core: two instances of a program that
each kept their own cache ask a provider twice for one answer, and two that
each keep their own count together ask at twice the rate one of them agreed to.
The second of those is what gets a key revoked.

Valkey speaks the same protocol and the same scripting, and is served by the
Redis adapter unchanged.

## What is where

| | |
| --- | --- |
| `redis` | Redis and Valkey: `cache.Store` and `rate.Limiter`, each atomic operation one script. |

The reference is [docs/reference/redis.md](docs/reference/redis.md).

A backend that needs no client library does not belong here. A filesystem
store is stdlib and belongs beside the in-process one in the runtime; nothing
has asked for one yet.
