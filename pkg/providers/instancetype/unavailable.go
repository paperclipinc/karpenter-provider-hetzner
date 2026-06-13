package instancetype

import (
	"sync"
	"time"
)

// unavailableCache tracks (serverType, location) offerings that recently failed
// to provision with a capacity error, so they are reported unavailable for a TTL.
type unavailableCache struct {
	ttl   time.Duration
	mu    sync.RWMutex
	items map[string]time.Time // key -> expiry
	nowFn func() time.Time
}

func newUnavailableCache(ttl time.Duration) *unavailableCache {
	return &unavailableCache{ttl: ttl, items: map[string]time.Time{}, nowFn: time.Now}
}

func unavailKey(serverType, location string) string { return serverType + "\x00" + location }

func (c *unavailableCache) markUnavailable(serverType, location string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[unavailKey(serverType, location)] = c.nowFn().Add(c.ttl)
}

func (c *unavailableCache) isUnavailable(serverType, location string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	exp, ok := c.items[unavailKey(serverType, location)]
	return ok && c.nowFn().Before(exp)
}
