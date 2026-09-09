# Redis reference

The Redis and Valkey adapter for the two ports in
[`effect-golang`](https://github.com/mbauer83/effect-golang/blob/main/docs/reference/cache.md)
a program reaches for when it reads something expensive.

```go
redis.Reaching[R](scope, options)   // Effect[R, redis.Fault, *goredis.Client]
redis.Keeping(client)               // *Store, a cache.Store
redis.Pacing(client)                // *Pace, a rate.Limiter
```

```go
effect.Scoped(func(scope effect.Scope) effect.Effect[R, redis.Fault, A] {
    return redis.Reaching[R](scope, &goredis.Options{Addr: "localhost:6379"}).
        FlatMap(func(client *goredis.Client) effect.Effect[R, redis.Fault, A] {
            return reading(redis.Keeping(client), redis.Pacing(client))
        })
})
```

## Why shared is the whole point

The runtime's own adapters are correct for a program that runs as one instance.
Four instances would each keep their own cache and ask a provider four times
for one answer, and each keep their own count and together ask at four times
the rate one of them agreed to. The second of those is what gets a key revoked.

So the reason to reach for this is not speed. It is that a rate limit is a
limit on the program, not on a process.

## The connection is a resource

`Reaching` is scoped, because a client owns connections it has to give back —
the same division `sql.Open` makes for the same reason: a handle with a `Close`
is a resource, and a resource belongs to a lifetime rather than to whoever
happened to make it.

The options are the caller's, whole. Addresses, credentials, pool sizes,
timeouts and TLS are how a deployment reaches a server and are not a property
of what is kept there, which is the division `web.Dial` makes by taking an
`http.Client`.

## Two scripts, because three round trips interleave

Reading, deciding and writing cannot be three commands when another instance is
doing the same thing between them. So each operation that has to be atomic is
one script.

**Keeping** writes the value and lists it under its subject in one call, so
there is no moment at which a value is kept and not listed. A listing that
missed it would be a value nobody could ask to have dropped, and whoever asked
would keep being served yesterday's answer however often they asked. The
listing outlives the value on purpose: a member naming an expired key costs one
deletion of nothing, and a listing that expired first would leave values
nothing could find to drop.

**Forgetting** reads the listing and deletes its members in one call, so a
value written while it runs is either kept whole or dropped whole. A page
showing half of yesterday's answers and half of today's would be the worst of
both.

**Reserving a turn** is the generic cell rate algorithm, the same one the
in-process limiter uses — which is what makes the two interchangeable: a
program tested against one behaves the same against the other, and the only
difference is who else can see the number.

The clock inside that script is the server's own, read with `TIME`. One count
shared by four containers needs one clock; containers whose clocks differ by a
second would each believe a different state.

## Valkey

The same protocol and the same scripting, served by this adapter unchanged.

## Deliberately absent

**A cluster-aware pace.** The reservation is one key, so it lives on one node
and is correct under Redis Cluster for that reason; a limiter sharded across
nodes would be several counts again, which is the thing this exists to avoid.

**Sentinel and cluster clients.** `goredis.Options` is what `Reaching` takes;
a failover or cluster client is a different constructor in that library, and
adding one here is a line when something asks for it.
