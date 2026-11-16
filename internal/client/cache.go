package client

import (
	"context"
	"strings"
	"sync"
)

// readCache memoizes read-only lookups for the life of one Client, which is one
// provider configuration: Terraform configures the provider afresh for every
// plan and every apply, so nothing cached here outlives the run that read it.
//
// A plan refreshes every resource, and most of what each refresh asks for is
// shared with its neighbours: the hierarchy entry of the account it sits in,
// the region list of that account, the Permissions on Demand requests at that
// scope. Without memoizing, an organization with a few thousand controls
// spends nearly all of a plan re-fetching the same few dozen answers through
// the rate limiter.
//
// The cache only serves reads until the first write. Once any mutation is
// sent it is switched off for the rest of the run, so every read after a
// write goes to the platform exactly as it would with no cache: a staged
// change can never be hidden behind an answer from before it. A plan sends no
// writes, so it gets the whole benefit; an apply mostly behaves as before.
//
// Concurrent lookups of the same key share one request, so the parallel
// refreshes Terraform runs do not all miss at once.
type readCache struct {
	mu       sync.Mutex
	disabled bool
	entries  map[string]*cacheEntry
}

type cacheEntry struct {
	done chan struct{}
	val  any
	err  error
}

func newReadCache(enabled bool) *readCache {
	return &readCache{disabled: !enabled, entries: map[string]*cacheEntry{}}
}

// get returns the cached result for key, calling fetch to produce it when
// there is none. Errors are never cached: callers sharing a failed request all
// see its error, and the next lookup tries again.
//
// fetch runs with the context of whichever caller arrived first. The others
// stop waiting when their own context ends.
func (rc *readCache) get(ctx context.Context, key string, fetch func(context.Context) (any, error)) (any, error) {
	rc.mu.Lock()
	if rc.disabled {
		rc.mu.Unlock()
		return fetch(ctx)
	}
	if entry, ok := rc.entries[key]; ok {
		rc.mu.Unlock()
		select {
		case <-entry.done:
			return entry.val, entry.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	entry := &cacheEntry{done: make(chan struct{})}
	rc.entries[key] = entry
	rc.mu.Unlock()

	entry.val, entry.err = fetch(ctx)

	rc.mu.Lock()
	if entry.err != nil && rc.entries[key] == entry {
		delete(rc.entries, key)
	}
	rc.mu.Unlock()
	close(entry.done)
	return entry.val, entry.err
}

// enabled reports whether lookups are being shared. Per-scope batch reads are
// only worth making when they are: without sharing, one fetches a whole
// account's list to answer a question about a single control.
func (rc *readCache) enabled() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return !rc.disabled
}

// disable switches the cache off for good and drops what it holds. Lookups
// already in flight finish and answer their own waiters, but nothing they
// return is served to a later caller.
func (rc *readCache) disable() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.disabled = true
	rc.entries = map[string]*cacheEntry{}
}

// isReadOnly reports whether a GraphQL document is a query. Anything else --
// a mutation, or a document this check does not recognize -- is treated as a
// write, which errs on the side of switching the cache off.
func isReadOnly(document string) bool {
	return strings.HasPrefix(strings.TrimSpace(document), "query ")
}

// cacheKey joins parts with a separator that cannot appear in a scope, id or
// control key.
func cacheKey(parts ...string) string {
	return strings.Join(parts, "\x00")
}
