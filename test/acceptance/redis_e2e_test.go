package acceptance

// The two ports, against a Redis.
//
// Miniredis rather than a container, because what is being stated is the
// behaviour of two scripts and of an expiry, and both are answered by a server
// that speaks the protocol and runs the Lua. It also lets the clock be moved,
// which a rate measured in seconds otherwise makes a test wait for.

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/mbauer83/effect-golang-cache/redis"
	"github.com/mbauer83/effect-golang/effect/cache"
	"github.com/mbauer83/effect-golang/effect/rate"
)

// The ports, satisfied by this adapter -- stated here so that a change to
// either is a compile failure rather than a wiring failure at start-up.
var (
	_ cache.Store  = (*redis.Store)(nil)
	_ rate.Limiter = (*redis.Pace)(nil)
)

// noon is the moment these tests pin the server's clock to.
//
// Pinned because the limiter reads Redis's own clock -- one count shared by
// four containers needs one clock -- so a test that holds that clock holds the
// limiter, and a wait of exactly one second can be asserted rather than
// approximated.
var noon = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

// shared is a Redis this test owns, and the two things a program keeps in one.
func shared(t *testing.T) (*miniredis.Miniredis, *redis.Store, *redis.Pace) {
	t.Helper()
	server := miniredis.RunT(t)
	server.SetTime(noon)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, redis.Keeping(client), redis.Pacing(client)
}

func clocked(server *miniredis.Miniredis, past time.Duration) {
	server.SetTime(noon.Add(past))
}

func kept(t *testing.T, store *redis.Store, key string) cache.Kept {
	t.Helper()
	held, err := store.Kept(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func keeping(t *testing.T, store *redis.Store, filing cache.Filing) {
	t.Helper()
	if err := store.Keep(context.Background(), filing); err != nil {
		t.Fatal(err)
	}
}

func TestAValueIsKeptUntilItStopsBeingWorthKeeping(t *testing.T) {
	server, store, _ := shared(t)

	keeping(t, store, cache.Filing{
		Key: "film:603", About: "tmdb:603", Entity: []byte(`{"said":"so"}`),
		Fresh: 10 * time.Minute,
	})

	if held := kept(t, store, "film:603"); !held.Found || string(held.Entity) != `{"said":"so"}` {
		t.Fatalf("expected the value back, got %+v", held)
	}

	server.FastForward(11 * time.Minute)

	if gone := kept(t, store, "film:603"); gone.Found {
		t.Fatalf("expected the value to have stopped being worth keeping, got %+v", gone)
	}
}

func TestAMissIsAnAnswerAndNotAFailure(t *testing.T) {
	// The ordinary state of a key nobody has asked for yet: a store that
	// failed on a miss would make every first request an error to handle.
	_, store, _ := shared(t)

	if held := kept(t, store, "film:nobody-asked"); held.Found || len(held.Entity) != 0 {
		t.Fatalf("expected nothing, got %+v", held)
	}
}

func TestEverythingAboutOneSubjectIsForgottenAtOnce(t *testing.T) {
	// What somebody asking for a thing to be looked up again means: not "drop
	// these four keys" but "find out about this thing again". So what is kept
	// says what it is about, whatever wrote it, and one call drops them all.
	_, store, _ := shared(t)
	filings := []cache.Filing{
		{Key: "tmdb:film:603", About: "tmdb:603", Entity: []byte(`{}`), Fresh: time.Hour},
		{Key: "letterboxd:film:603", About: "tmdb:603", Entity: []byte(`<html>`), Fresh: time.Hour},
		{Key: "tmdb:film:604", About: "tmdb:604", Entity: []byte(`{}`), Fresh: time.Hour},
	}
	for _, filing := range filings {
		keeping(t, store, filing)
	}

	if err := store.Forget(context.Background(), "tmdb:603"); err != nil {
		t.Fatal(err)
	}

	for _, filing := range filings[:2] {
		if gone := kept(t, store, filing.Key); gone.Found {
			t.Fatalf("expected %q to have been forgotten", filing.Key)
		}
	}
	if still := kept(t, store, filings[2].Key); !still.Found {
		t.Fatal("expected what is kept about another subject to stay")
	}
}

func TestForgettingWhatWasNeverKeptIsNotAFailure(t *testing.T) {
	// Somebody asking for a thing nobody has read yet to be read again is
	// asking for something reasonable, and the answer is that there was
	// nothing to drop.
	_, store, _ := shared(t)

	if err := store.Forget(context.Background(), "tmdb:999"); err != nil {
		t.Fatalf("expected forgetting nothing to be no failure, got %v", err)
	}
}

func TestAFilingWithNoLifetimeIsRefusedRatherThanKeptForever(t *testing.T) {
	_, store, _ := shared(t)

	err := store.Keep(context.Background(), cache.Filing{Key: "film:603", Entity: []byte("x")})

	if err == nil {
		t.Fatal("expected a filing with no lifetime to be refused")
	}
	if held := kept(t, store, "film:603"); held.Found {
		t.Fatal("expected nothing kept")
	}
}

// thrice is an allowance of three every three seconds: a spacing of one
// second, and a burst of three.
func thrice() rate.Allowance {
	return rate.Allowance{Name: "a service", Most: 3, Every: 3 * time.Second}
}

func turned(t *testing.T, limiter *redis.Pace, allowance rate.Allowance) time.Duration {
	t.Helper()
	wait, err := limiter.Turn(context.Background(), allowance)
	if err != nil {
		t.Fatal(err)
	}
	return wait
}

func TestTheBurstAnAllowanceToleratesGoesAtOnce(t *testing.T) {
	_, _, limiter := shared(t)

	for turn := range 3 {
		if wait := turned(t, limiter, thrice()); wait != 0 {
			t.Fatalf("expected turn %d of the burst to go at once, waits %v", turn+1, wait)
		}
	}
}

func TestPastTheBurstEveryTurnIsSpaced(t *testing.T) {
	// What "three every three seconds" means to whoever is being asked: the
	// fourth waits a spacing and the fifth two, and nothing is exceeded and
	// then apologised for.
	_, _, limiter := shared(t)
	for range 3 {
		_ = turned(t, limiter, thrice())
	}

	for turn, expected := range []time.Duration{time.Second, 2 * time.Second} {
		if wait := turned(t, limiter, thrice()); wait != expected {
			t.Fatalf("expected turn %d past the burst to wait %v, waits %v",
				turn+4, expected, wait)
		}
	}
}

func TestASpentAllowanceComesBackOneTurnAtATime(t *testing.T) {
	// Not a window that empties all at once, which is what keeps a burst from
	// arriving on every boundary.
	server, _, limiter := shared(t)
	for range 4 {
		_ = turned(t, limiter, thrice())
	}

	clocked(server, 2*time.Second)

	if wait := turned(t, limiter, thrice()); wait != 0 {
		t.Fatalf("expected the turn to have come round, waits %v", wait)
	}
}

func TestTwoAllowancesAreCountedApart(t *testing.T) {
	_, _, limiter := shared(t)
	other := rate.Allowance{Name: "another service", Most: 3, Every: 3 * time.Second}
	for range 4 {
		_ = turned(t, limiter, thrice())
	}

	if wait := turned(t, limiter, other); wait != 0 {
		t.Fatalf("expected the other service's own allowance, waits %v", wait)
	}
}

func TestAnUnstatedAllowanceIsRefusedRatherThanTreatedAsUnlimited(t *testing.T) {
	_, _, limiter := shared(t)

	_, err := limiter.Turn(context.Background(), rate.Allowance{Name: "a service"})

	if err == nil {
		t.Fatal("expected an unstated allowance to be refused")
	}
}
