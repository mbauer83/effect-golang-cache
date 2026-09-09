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

// onMiniredis is a Redis this test owns, and the two things a program keeps in one.
func onMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Store, *redis.Pace) {
	t.Helper()
	server := miniredis.RunT(t)
	server.SetTime(noon)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, redis.NewStore(client), redis.NewPace(client)
}

func advanceClock(server *miniredis.Miniredis, past time.Duration) {
	server.SetTime(noon.Add(past))
}

func readBack(t *testing.T, store *redis.Store, key string) cache.Cached {
	t.Helper()
	cached, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return cached
}

func putEntry(t *testing.T, store *redis.Store, filing cache.Entry) {
	t.Helper()
	if err := store.Put(context.Background(), filing); err != nil {
		t.Fatal(err)
	}
}

func TestAValueIsKeptUntilItStopsBeingWorthKeeping(t *testing.T) {
	server, store, _ := onMiniredis(t)

	putEntry(t, store, cache.Entry{
		Key: "film:603", About: "tmdb:603", Entity: []byte(`{"said":"so"}`),
		Fresh: 10 * time.Minute,
	})

	if cached := readBack(t, store, "film:603"); !cached.Found || string(cached.Entity) != `{"said":"so"}` {
		t.Fatalf("expected the value back, got %+v", cached)
	}

	server.FastForward(11 * time.Minute)

	if gone := readBack(t, store, "film:603"); gone.Found {
		t.Fatalf("expected the value to have stopped being worth keeping, got %+v", gone)
	}
}

func TestAMissIsAnAnswerAndNotAFailure(t *testing.T) {
	// The ordinary state of a key nobody has asked for yet: a store that
	// failed on a miss would make every first request an error to handle.
	_, store, _ := onMiniredis(t)

	if cached := readBack(t, store, "film:nobody-asked"); cached.Found || len(cached.Entity) != 0 {
		t.Fatalf("expected nothing, got %+v", cached)
	}
}

func TestEverythingAboutOneSubjectIsForgottenAtOnce(t *testing.T) {
	// What somebody asking for a thing to be looked up again means: not "drop
	// these four keys" but "find out about this thing again". So what is kept
	// says what it is about, whatever wrote it, and one call drops them all.
	_, store, _ := onMiniredis(t)
	filings := []cache.Entry{
		{Key: "tmdb:film:603", About: "tmdb:603", Entity: []byte(`{}`), Fresh: time.Hour},
		{Key: "letterboxd:film:603", About: "tmdb:603", Entity: []byte(`<html>`), Fresh: time.Hour},
		{Key: "tmdb:film:604", About: "tmdb:604", Entity: []byte(`{}`), Fresh: time.Hour},
	}
	for _, filing := range filings {
		putEntry(t, store, filing)
	}

	if err := store.Invalidate(context.Background(), "tmdb:603"); err != nil {
		t.Fatal(err)
	}

	for _, filing := range filings[:2] {
		if gone := readBack(t, store, filing.Key); gone.Found {
			t.Fatalf("expected %q to have been forgotten", filing.Key)
		}
	}
	if still := readBack(t, store, filings[2].Key); !still.Found {
		t.Fatal("expected what is kept about another subject to stay")
	}
}

func TestForgettingWhatWasNeverKeptIsNotAFailure(t *testing.T) {
	// Somebody asking for a thing nobody has read yet to be read again is
	// asking for something reasonable, and the answer is that there was
	// nothing to drop.
	_, store, _ := onMiniredis(t)

	if err := store.Invalidate(context.Background(), "tmdb:999"); err != nil {
		t.Fatalf("expected forgetting nothing to be no failure, got %v", err)
	}
}

func TestAFilingWithNoLifetimeIsRefusedRatherThanKeptForever(t *testing.T) {
	_, store, _ := onMiniredis(t)

	err := store.Put(context.Background(), cache.Entry{Key: "film:603", Entity: []byte("x")})

	if err == nil {
		t.Fatal("expected a filing with no lifetime to be refused")
	}
	if cached := readBack(t, store, "film:603"); cached.Found {
		t.Fatal("expected nothing kept")
	}
}

// threePerSecond is an allowance of three every three seconds: a spacing of one
// second, and a burst of three.
func threePerSecond() rate.Allowance {
	return rate.Allowance{Name: "a service", Most: 3, Every: 3 * time.Second}
}

func timeOneTurn(t *testing.T, limiter *redis.Pace, allowance rate.Allowance) time.Duration {
	t.Helper()
	wait, err := limiter.Turn(context.Background(), allowance)
	if err != nil {
		t.Fatal(err)
	}
	return wait
}

func TestTheBurstAnAllowanceToleratesGoesAtOnce(t *testing.T) {
	_, _, limiter := onMiniredis(t)

	for turn := range 3 {
		if wait := timeOneTurn(t, limiter, threePerSecond()); wait != 0 {
			t.Fatalf("expected turn %d of the burst to go at once, waits %v", turn+1, wait)
		}
	}
}

func TestPastTheBurstEveryTurnIsSpaced(t *testing.T) {
	// What "three every three seconds" means to whoever is being asked: the
	// fourth waits a spacing and the fifth two, and nothing is exceeded and
	// then apologised for.
	_, _, limiter := onMiniredis(t)
	for range 3 {
		_ = timeOneTurn(t, limiter, threePerSecond())
	}

	for turn, expected := range []time.Duration{time.Second, 2 * time.Second} {
		if wait := timeOneTurn(t, limiter, threePerSecond()); wait != expected {
			t.Fatalf("expected turn %d past the burst to wait %v, waits %v",
				turn+4, expected, wait)
		}
	}
}

func TestASpentAllowanceComesBackOneTurnAtATime(t *testing.T) {
	// Not a window that empties all at once, which is what keeps a burst from
	// arriving on every boundary.
	server, _, limiter := onMiniredis(t)
	for range 4 {
		_ = timeOneTurn(t, limiter, threePerSecond())
	}

	advanceClock(server, 2*time.Second)

	if wait := timeOneTurn(t, limiter, threePerSecond()); wait != 0 {
		t.Fatalf("expected the turn to have come round, waits %v", wait)
	}
}

func TestTwoAllowancesAreCountedApart(t *testing.T) {
	_, _, limiter := onMiniredis(t)
	other := rate.Allowance{Name: "another service", Most: 3, Every: 3 * time.Second}
	for range 4 {
		_ = timeOneTurn(t, limiter, threePerSecond())
	}

	if wait := timeOneTurn(t, limiter, other); wait != 0 {
		t.Fatalf("expected the other service's own allowance, waits %v", wait)
	}
}

func TestAnUnstatedAllowanceIsRefusedRatherThanTreatedAsUnlimited(t *testing.T) {
	_, _, limiter := onMiniredis(t)

	_, err := limiter.Turn(context.Background(), rate.Allowance{Name: "a service"})

	if err == nil {
		t.Fatal("expected an unstated allowance to be refused")
	}
}
