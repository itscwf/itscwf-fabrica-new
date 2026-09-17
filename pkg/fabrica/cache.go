package fabrica

import (
	"sync"
	"time"
)

// TTLCache is the tiny read-through cache used to protect the Hermes CLI
// endpoints: spawning `hermes` costs seconds, while the dashboard polls.
type TTLCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]item
}

type item struct {
	value   any
	expires time.Time
}

// NewTTLCache returns a cache with the given TTL. A TTL <= 0 disables it.
func NewTTLCache(ttl time.Duration) *TTLCache {
	return &TTLCache{ttl: ttl, items: make(map[string]item)}
}

// Get returns a cached value when it is still fresh.
func (c *TTLCache) Get(key string) (any, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok || time.Now().After(it.expires) {
		delete(c.items, key)
		return nil, false
	}
	return it.value, true
}

// Set stores a value for the configured TTL.
func (c *TTLCache) Set(key string, value any) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = item{value: value, expires: time.Now().Add(c.ttl)}
}
