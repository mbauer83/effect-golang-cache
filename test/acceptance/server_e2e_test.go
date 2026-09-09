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

// really is a real server, and the two things a program keeps in one.
//
// The keys are named for the test that made them and dropped afterwards, so
// two runs against one server do not share state and a failed run leaves
// nothing behind.
func really(t *testing.T) (*redis.Store, *redis.Pace, string) {
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
	named := t.Name()
	t.Cleanup(func() {
		// Everything this test could have written, by the names it writes
		// under: the values, the listing of what they are about, and the
		// count of what was asked.
		_ = client.Del(context.Background(),
			named+":one", named+":two", "about:"+named, "pace:"+named).Err()
	})
	return redis.Keeping(client), redis.Pacing(client), named
}

func TestOnARealServerAValueIsKeptAndReadBack(t *testing.T) {
	store, _, named := really(t)
	within := context.Background()

	if err := store.Put(within, cache.Entry{
		Key: named + ":one", About: named, Entity: []byte(`{"said":"so"}`),
		Fresh: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	held, err := store.Get(within, named+":one")
	if err != nil {
		t.Fatal(err)
	}
	if !held.Found || string(held.Entity) != `{"said":"so"}` {
		t.Fatalf("expected the value back, got %+v", held)
	}
}

func TestOnARealServerAMissIsAnAnswer(t *testing.T) {
	store, _, named := really(t)

	held, err := store.Get(context.Background(), named+":nobody-asked")

	if err != nil {
		t.Fatalf("expected a miss to be an answer, got %v", err)
	}
	if held.Found {
		t.Fatalf("expected nothing, got %+v", held)
	}
}

func TestOnARealServerEverythingAboutOneSubjectIsForgottenAtOnce(t *testing.T) {
	// The script reads a set and deletes its members. Whether SMEMBERS of a
	// set that has expired, and DEL of keys that are already gone, behave the
	// way the script assumes is a question about the server.
	store, _, named := really(t)
	within := context.Background()
	for _, key := range []string{named + ":one", named + ":two"} {
		if err := store.Put(within, cache.Entry{
			Key: key, About: named, Entity: []byte(`{}`), Fresh: time.Minute,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.Invalidate(within, named); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{named + ":one", named + ":two"} {
		held, err := store.Get(within, key)
		if err != nil {
			t.Fatal(err)
		}
		if held.Found {
			t.Fatalf("expected %q to have been forgotten", key)
		}
	}
}

func TestOnARealServerForgettingWhatWasNeverKeptIsNotAFailure(t *testing.T) {
	store, _, named := really(t)

	if err := store.Invalidate(context.Background(), named+":nothing-here"); err != nil {
		t.Fatalf("expected forgetting nothing to be no failure, got %v", err)
	}
}

func TestOnARealServerTheBurstGoesAtOnceAndTheRestIsSpaced(t *testing.T) {
	// The whole of the reservation, on the server's own clock: three every
	// three seconds means three at once and then one a second. Asserted as a
	// bound rather than an equality, because the clock here is real -- which
	// is exactly why the equality is asserted against the fake instead.
	_, pace, named := really(t)
	allowance := rate.Allowance{Name: named, Most: 3, Every: 3 * time.Second}
	within := context.Background()

	for turn := range 3 {
		wait, err := pace.Turn(within, allowance)
		if err != nil {
			t.Fatal(err)
		}
		if wait != 0 {
			t.Fatalf("expected turn %d of the burst to go at once, waits %v", turn+1, wait)
		}
	}

	fourth, err := pace.Turn(within, allowance)
	if err != nil {
		t.Fatal(err)
	}
	if fourth < 900*time.Millisecond || fourth > time.Second {
		t.Fatalf("expected the fourth turn to wait about a second, waits %v", fourth)
	}
	fifth, err := pace.Turn(within, allowance)
	if err != nil {
		t.Fatal(err)
	}
	if fifth < fourth {
		t.Fatalf("expected the fifth to wait longer than the fourth, %v then %v",
			fourth, fifth)
	}
}

func TestOnARealServerAnUnstatedAllowanceIsRefused(t *testing.T) {
	_, pace, named := really(t)

	_, err := pace.Turn(context.Background(), rate.Allowance{Name: named})

	if err == nil {
		t.Fatal("expected an unstated allowance to be refused")
	}
}
