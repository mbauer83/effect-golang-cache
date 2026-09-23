package redis

// Where a program keeps what it has already found out.

import (
	"context"
	"errors"

	goredis "github.com/redis/go-redis/v9"

	"github.com/mbauer83/effect-golang/effect/cache"
)

// Store keeps values where every instance of a program can see them.
type Store struct {
	client *goredis.Client
}

// NewStore is the store over a connection.
func NewStore(client *goredis.Client) *Store { return &Store{client: client} }

// Get is what is held under a key, and whether anything is.
//
// A key that has expired and a key that was never written are one answer, which
// is the only thing a cache can say about either: what it holds is what is
// still worth having.
func (store *Store) Get(ctx context.Context, key string) (cache.Lookup, error) {
	entity, err := store.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return cache.Lookup{}, nil
	}
	if err != nil {
		return cache.Lookup{}, Fault{Op: "get", Key: key, Err: err}
	}
	return cache.Lookup{Entity: entity, Found: true}, nil
}

// Put stores a value for as long as it is worth keeping, and notes it among
// what is known about its subject.
//
// One script, so there is no moment at which a value is kept and not listed
// under what it is about. A listing that missed it would be a value nobody
// could ask to have dropped, and whoever asked would keep being served
// yesterday's answer however often they asked.
func (store *Store) Put(ctx context.Context, entry cache.Entry) error {
	if !entry.IsStorable() {
		return Fault{Op: "put", Key: entry.Key, Err: cache.ErrUnworthy}
	}
	err := putScript.Run(ctx, store.client,
		[]string{entry.Key, subjectKey(entry.About)},
		entry.Entity,
		entry.Fresh.Milliseconds(),
	).Err()
	if err != nil {
		return Fault{Op: "put", Key: entry.Key, Err: err}
	}
	return nil
}

// Invalidate drops every entry about one subject, whatever wrote it.
func (store *Store) Invalidate(ctx context.Context, subject string) error {
	if err := invalidateScript.Run(ctx, store.client, []string{subjectKey(subject)}).Err(); err != nil {
		return Fault{Op: "invalidate", Key: subject, Err: err}
	}
	return nil
}

// subjectKey is where the keys of what is known about one subject are listed.
func subjectKey(subject string) string { return "about:" + subject }

// putScript keeps the value and lists it under its subject.
//
// The listing outlives the value on purpose: a member naming a key that has
// already expired costs one deletion of nothing, and the alternative -- a
// listing that expired first -- would leave values nothing could find to drop.
//
// A subject of "" is not listed. A value nothing will ever ask to have dropped
// needs no listing, and an empty subject would otherwise collect every such
// value in one growing set.
var putScript = goredis.NewScript(`
local fresh = tonumber(ARGV[2])
redis.call('SET', KEYS[1], ARGV[1], 'PX', fresh)
if KEYS[2] ~= 'about:' then
  redis.call('SADD', KEYS[2], KEYS[1])
  redis.call('PEXPIRE', KEYS[2], fresh * 2)
end
return 1
`)

// invalidateScript deletes every value listed about one subject, and the listing.
//
// In one script rather than a read followed by deletions, so a value written
// while this was running is either kept whole or dropped whole: a page that
// showed half of yesterday's answers and half of today's would be the worst of
// both.
var invalidateScript = goredis.NewScript(`
local members = redis.call('SMEMBERS', KEYS[1])
for at = 1, #members do
  redis.call('DEL', members[at])
end
redis.call('DEL', KEYS[1])
return #members
`)
