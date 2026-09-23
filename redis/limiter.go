package redis

// Where a program keeps the count of how often it has asked.

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/mbauer83/effect-golang/effect/rate"
)

// Limiter hands out turns to every instance of a program from one count.
type Limiter struct {
	client *goredis.Client
}

// NewLimiter is the limiter over a connection.
func NewLimiter(client *goredis.Client) *Limiter { return &Limiter{client: client} }

// Turn reserves the next turn under an allowance and says how long until it.
//
// Reserved rather than checked, which is the whole point of doing this in a
// script: a check would tell every one of ten concurrent callers that there is
// room, and three round trips would interleave with another instance's.
//
// The ceiling is applied in the same script for the same reason. A caller that
// read the queue, decided it was too long and then did not reserve would have
// read a number another instance had already changed -- and a caller that
// reserved first and declined afterwards would have spent an allowance on a
// request it never made, pushing back every instance behind it. Deciding and
// reserving are one operation here because that is the only place they can be
// one.
//
// The clock is the server's own, read inside the script, because one count
// shared by four containers needs one clock: containers whose clocks differ by
// a second would each believe a different state.
func (limiter *Limiter) Turn(
	ctx context.Context,
	allowance rate.Allowance,
	longest time.Duration,
) (time.Duration, error) {
	if !allowance.IsStated() {
		return 0, rate.Fault{Allowance: allowance.Name, Err: rate.ErrUnstated}
	}
	reply, err := reserveScript.Run(ctx, limiter.client,
		[]string{"pace:" + allowance.Name},
		allowance.Spacing().Milliseconds(),
		allowance.Most,
		ceilingOf(longest),
	).Int64Slice()
	if err != nil {
		return 0, Fault{Op: "take a turn", Key: allowance.Name, Err: err}
	}
	if len(reply) != 2 {
		return 0, Fault{Op: "take a turn", Key: allowance.Name, Err: errUnreadableTurn}
	}
	wait := time.Duration(reply[0]) * time.Millisecond
	if reserved := reply[1] == 1; !reserved {
		return wait, rate.Fault{Allowance: allowance.Name, Err: rate.ErrLimitExceeded}
	}
	return wait, nil
}

// ceilingOf is a ceiling as the script takes one: milliseconds, and a negative
// for no ceiling.
//
// Zero is no ceiling, which is the contract rate.Terms already states about
// how long a caller will queue -- and the script is told it as a negative
// rather than a zero so that its own comparison needs no special case.
func ceilingOf(longest time.Duration) int64 {
	if longest <= 0 {
		return -1
	}
	return longest.Milliseconds()
}

var errUnreadableTurn = errors.New("the pacing script answered with something other than a wait and whether it reserved")

// reserveScript is the generic cell rate algorithm, which is what a rate limit that
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
var reserveScript = goredis.NewScript(`
local spacing = tonumber(ARGV[1])
local burst   = tonumber(ARGV[2])
local ceiling = tonumber(ARGV[3])

local clock = redis.call('TIME')
local now = clock[1] * 1000 + math.floor(clock[2] / 1000)

local arriving = tonumber(redis.call('GET', KEYS[1]) or '0')
if arriving < now then arriving = now end

local tolerance = (burst - 1) * spacing
local wait = arriving - tolerance - now
if wait < 0 then wait = 0 end

-- The ceiling is checked before the moment is claimed, so a caller that will
-- not wait leaves the count exactly as it found it.
if ceiling >= 0 and wait > ceiling then
  return { wait, 0 }
end

local claimed = arriving + spacing
redis.call('SET', KEYS[1], claimed, 'PX', math.ceil(claimed - now) + spacing)
return { wait, 1 }
`)
