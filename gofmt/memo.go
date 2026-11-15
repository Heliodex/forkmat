package main

import "sync"

type (
	// memoEntry is the result for one key.
	memoEntry[V any] struct {
		once sync.Once
		v V
	}

	// This file holds memo, the cache that the -strict passes share between the files of a run. Each key is computed once, by whichever file asks first. The other files asking for the same key wait for that one result, and files asking for other keys are not held up: a single lock around the whole cache would serialise all the files of a large tree. A key's computation must not ask the same memo for that key again.

	memo[K comparable, V any] struct {
		mu sync.Mutex
		m map[K]*memoEntry[V]
	}
)

// get returns the value for key, computing it with compute the first time.
func (c *memo[K, V]) get(key K, compute func() V) V {
	c.mu.Lock()
	if c.m == nil {
		c.m = map[K]*memoEntry[V]{}
	}

	e, ok := c.m[key]
	if !ok {
		e = &memoEntry[V]{}
		c.m[key] = e
	}
	c.mu.Unlock()
	e.once.Do(func() {
		e.v = compute()
	})
	return e.v
}
