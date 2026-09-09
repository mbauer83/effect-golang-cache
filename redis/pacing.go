package redis

// Where a program keeps the count of how often it has asked.

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/mbauer83/effect-golang/effect/rate"
)

// Pace hands out turns to every instance of a program from one count.
type Pace struct {
	client *goredis.Client
}

// Pacing is the limiter over a connection.
func Pacing(client *goredis.Client) *Pace { return &Pace{client: client} }

// Turn reserves the next turn under an allowance and says how long until it.
//
// Reserved rather than checked, which is the whole point of doing this in a
// script: a check would tell every one of ten concurrent callers that there is
// room, and three round trips would interleave with another instance's.
//
// The clock is the server's own, read inside the script, because one count
// shared by four containers needs one clock: containers whose clocks differ by
// a second would each believe a different state.
func (limiter *Pace) Turn(
	ctx context.Context,
	allowance rate.Allowance,
) (time.Duration, error) {
	if !allowance.IsStated() {
		return 0, rate.Fault{Allowance: allowance.Name, Err: rate.ErrUnstated}
	}
	waited, err := reserve.Run(ctx, limiter.client,
		[]string{"pace:" + allowance.Name},
		allowance.Spacing().Milliseconds(),
		allowance.Most,
	).Int64()
	if err != nil {
		return 0, Fault{Doing: "taking a turn under " + allowance.Name, Err: err}
	}
	return time.Duration(waited) * time.Millisecond, nil
}

// reserve is the generic cell rate algorithm, which is what a rate limit that
// must not be exceeded looks like written down.
//
// One number is kept per allowance: the moment the next request would arrive
// at if requests were spaced evenly. A caller moves that moment along by one
// spacing and is told to wait until the moment it just claimed, less the burst
// the allowance tolerates. So the first Most requests in a period go at once
// and everything after them is spaced, and a spent allowance comes back one
// turn at a time rather than all at once on a window boundary.
//
// The same algorithm as the in-process limiter in core, which is what makes
// the two interchangeable: a program tested against one behaves the same
// against the other, and the only difference is who else can see the number.
var reserve = goredis.NewScript(`
local spacing = tonumber(ARGV[1])
local burst   = tonumber(ARGV[2])

local clock = redis.call('TIME')
local now = clock[1] * 1000 + math.floor(clock[2] / 1000)

local arriving = tonumber(redis.call('GET', KEYS[1]) or '0')
if arriving < now then arriving = now end

local tolerance = (burst - 1) * spacing
local wait = arriving - tolerance - now
if wait < 0 then wait = 0 end

local claimed = arriving + spacing
redis.call('SET', KEYS[1], claimed, 'PX', math.ceil(claimed - now) + spacing)
return wait
`)
