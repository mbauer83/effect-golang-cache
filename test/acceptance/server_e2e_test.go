package acceptance

// The two scripts, against a server that really runs Lua.
//
// The suite beside this one runs them on gopher-lua inside a fake, which is
// worth having: it lets a test hold the clock, and a rate measured in seconds
// otherwise makes a test wait for one. What it cannot say is that the scripts
// behave the same on the thing they were written for -- SMEMBERS of a set that
// was never created, an expiry in milliseconds, the server's own clock, the
// return of a table from a script -- so these say it.
//
// Gated on an address, because a server is not something a test suite may
// assume: set EFFECT_GOLANG_REDIS_URL to run them. Only the cases that need no
// control of the clock are here; the ones that move time stay where time can
// be moved.

import (
	"context"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/mbauer83/effect-golang-cache/redis"
	"github.com/mbauer83/effect-golang/effect/cache"
	"github.com/mbauer83/effect-golang/effect/rate"
)

// connectRealServer is a real server, and the two things a program keeps in one.
//
// The keys are prefix for the test that made them and dropped afterwards, so
// two runs against one server do not share state and a failed run leaves
// nothing behind.
func connectRealServer(t *testing.T) (*redis.Store, *redis.Limiter, string) {
	t.Helper()
	address := os.Getenv("EFFECT_GOLANG_REDIS_URL")
	if address == "" {
		t.Skip("set EFFECT_GOLANG_REDIS_URL to run this against a server")
	}
	options, err := goredis.ParseURL(address)
	if err != nil {
		t.Fatal(err)
	}
	client := goredis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	prefix := t.Name()
	t.Cleanup(func() {
		// Everything this test could have written, by the names it writes
		// under: the values, the listing of what they are about, and the
		// count of what was asked.
		_ = client.Del(context.Background(),
			prefix+":one", prefix+":two", "about:"+prefix, "pace:"+prefix).Err()
	})
	return redis.NewStore(client), redis.NewLimiter(client), prefix
}

func TestOnARealServerAValueIsKeptAndReadBack(t *testing.T) {
	store, _, prefix := connectRealServer(t)
	ctx := context.Background()

	if err := store.Put(ctx, cache.Entry{
		Key: prefix + ":one", About: prefix, Entity: []byte(`{"said":"so"}`),
		Fresh: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	lookup, err := store.Get(ctx, prefix+":one")
	if err != nil {
		t.Fatal(err)
	}
	if !lookup.Found || string(lookup.Entity) != `{"said":"so"}` {
		t.Fatalf("expected the value back, got %+v", lookup)
	}
}

func TestOnARealServerAMissIsAnAnswer(t *testing.T) {
	store, _, prefix := connectRealServer(t)

	lookup, err := store.Get(context.Background(), prefix+":nobody-asked")

	if err != nil {
		t.Fatalf("expected a miss to be an answer, got %v", err)
	}
	if lookup.Found {
		t.Fatalf("expected nothing, got %+v", lookup)
	}
}

func TestOnARealServerEverythingAboutOneSubjectIsForgottenAtOnce(t *testing.T) {
	// The script reads a set and deletes its members. Whether SMEMBERS of a
	// set that has expired, and DEL of keys that are already gone, behave the
	// way the script assumes is a question about the server.
	store, _, prefix := connectRealServer(t)
	ctx := context.Background()
	for _, key := range []string{prefix + ":one", prefix + ":two"} {
		if err := store.Put(ctx, cache.Entry{
			Key: key, About: prefix, Entity: []byte(`{}`), Fresh: time.Minute,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.Invalidate(ctx, prefix); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{prefix + ":one", prefix + ":two"} {
		lookup, err := store.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if lookup.Found {
			t.Fatalf("expected %q to have been forgotten", key)
		}
	}
}

func TestOnARealServerForgettingWhatWasNeverKeptIsNotAFailure(t *testing.T) {
	store, _, prefix := connectRealServer(t)

	if err := store.Invalidate(context.Background(), prefix+":nothing-here"); err != nil {
		t.Fatalf("expected forgetting nothing to be no failure, got %v", err)
	}
}

func TestOnARealServerTheBurstGoesAtOnceAndTheRestIsSpaced(t *testing.T) {
	// The whole of the reservation, on the server's own clock: three every
	// three seconds means three at once and then one a second. Asserted as a
	// bound rather than an equality, because the clock here is real -- which
	// is exactly why the equality is asserted against the fake instead.
	_, limiter, prefix := connectRealServer(t)
	allowance := rate.Allowance{Name: prefix, Most: 3, Every: 3 * time.Second}
	ctx := context.Background()

	for turn := range 3 {
		wait, err := limiter.Turn(ctx, allowance, 0)
		if err != nil {
			t.Fatal(err)
		}
		if wait != 0 {
			t.Fatalf("expected turn %d of the burst to go at once, waits %v", turn+1, wait)
		}
	}

	fourth, err := limiter.Turn(ctx, allowance, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fourth < 900*time.Millisecond || fourth > time.Second {
		t.Fatalf("expected the fourth turn to wait about a second, waits %v", fourth)
	}
	fifth, err := limiter.Turn(ctx, allowance, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fifth < fourth {
		t.Fatalf("expected the fifth to wait longer than the fourth, %v then %v",
			fourth, fifth)
	}
}

func TestOnARealServerAnUnstatedAllowanceIsRefused(t *testing.T) {
	_, limiter, prefix := connectRealServer(t)

	_, err := limiter.Turn(context.Background(), rate.Allowance{Name: prefix}, 0)

	if err == nil {
		t.Fatal("expected an unstated allowance to be refused")
	}
}
