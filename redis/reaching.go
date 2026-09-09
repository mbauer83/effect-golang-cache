// Package redis keeps, for every instance of a program at once, what one
// instance would otherwise keep to itself.
//
// Two ports, one server, and they are here together because the reason for
// both is the same: a program that runs as four containers has one cache and
// one rate limit, not four. Four caches ask a provider four times for one
// answer; four counts together ask at four times the rate one of them agreed
// to, and the second of those is what gets a key revoked.
//
// The scripts are where the care is. Reading, deciding and writing cannot be
// three round trips when another instance is doing the same thing between
// them, so each of the two operations that has to be atomic is one script.
//
// Valkey speaks the same protocol and the same scripting, and is served by
// this adapter unchanged.
package redis

import (
	"context"

	goredis "github.com/redis/go-redis/v9"

	"github.com/mbauer83/effect-golang/effect"
)

// Fault is why this adapter could not answer.
//
// Its own type rather than one of the ports': a caller holding one of these
// knows which server could not be reached, and a caller that only sees the
// port's fault knows that its cache failed -- which is the more useful thing
// to log and the less useful thing to act on. Both are available, because the
// port's fault wraps this one.
type Fault struct {
	Doing string
	Err   error
}

func (fault Fault) Error() string {
	said := "redis: " + fault.Doing
	if fault.Err != nil {
		said += ": " + fault.Err.Error()
	}
	return said
}

func (fault Fault) Unwrap() error { return fault.Err }

// Connect is a connection for as long as the scope holds it.
//
// Scoped because a client owns connections it has to give back, which is the
// division sql.Open makes for the same reason: a handle with a Close is a
// resource and a resource belongs to a lifetime, not to whoever happened to
// make it.
//
// The options are the caller's, whole. Addresses, credentials, pool sizes,
// timeouts and TLS are how a deployment reaches a server and are not a
// property of what is kept there -- the same division web.Dial makes by taking
// an http.Client.
func Connect[R any](
	scope effect.Scope,
	options *goredis.Options,
) effect.Effect[R, Fault, *goredis.Client] {
	acquire := effect.Try(
		func(ctx context.Context, _ R) (*goredis.Client, error) {
			client := goredis.NewClient(options)
			if err := client.Ping(ctx).Err(); err != nil {
				// The client is useless and would otherwise hold whatever it
				// managed to open.
				_ = client.Close()
				return nil, err
			}
			return client, nil
		},
		func(err error) Fault { return Fault{Doing: "connecting", Err: err} },
	).Named("reaching redis")

	return scope.AcquireRelease(acquire, disconnect[R])
}

func disconnect[R any](client *goredis.Client) effect.Effect[R, effect.Never, effect.Unit] {
	return effect.AddFinalizer[R](func(context.Context) error { return client.Close() })
}
