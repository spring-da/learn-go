package main

import (
	"errors"
	"sync"
	"time"
)

type cacheEntry[V any] struct {
	value     V
	expiresAt time.Time
}

type CacheStats struct {
	Entries     int    `json:"entries"`
	Capacity    int    `json:"capacity"`
	TTLMillis   int64  `json:"ttl_ms"`
	Hits        uint64 `json:"hits"`
	Misses      uint64 `json:"misses"`
	Evictions   uint64 `json:"evictions"`
	Expirations uint64 `json:"expirations"`
}

// K 必须能作为 map 的键；V 的类型由实例化时指定，不需要 any 类型断言。
type TTLCache[K comparable, V any] struct {
	mu       sync.Mutex
	items    map[K]cacheEntry[V]
	order    []K
	capacity int
	ttl      time.Duration
	stats    CacheStats
}

func NewTTLCache[K comparable, V any](capacity int, ttl time.Duration) (*TTLCache[K, V], error) {
	if capacity <= 0 || ttl <= 0 {
		return nil, errors.New("cache capacity and TTL must be positive")
	}
	return &TTLCache[K, V]{items: make(map[K]cacheEntry[V], capacity), order: make([]K, 0, capacity), capacity: capacity, ttl: ttl}, nil
}

// 显式时间便于精确测试过期边界；生产调用传入 time.Now()，保留单调时钟信息。
func (c *TTLCache[K, V]) GetAt(key K, now time.Time) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.items[key]
	if found && now.Before(entry.expiresAt) {
		c.stats.Hits++
		return entry.value, true
	}
	if found {
		c.removeLocked(key)
		c.stats.Expirations++
	}
	c.stats.Misses++
	var zero V
	return zero, false
}

func (c *TTLCache[K, V]) PutAt(key K, value V, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purgeExpiredLocked(now)
	if _, found := c.items[key]; found {
		c.removeLocked(key)
	}
	if len(c.items) == c.capacity {
		c.removeLocked(c.order[0])
		c.stats.Evictions++
	}
	c.items[key] = cacheEntry[V]{value: value, expiresAt: now.Add(c.ttl)}
	c.order = append(c.order, key)
}

func (c *TTLCache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(key)
}

func (c *TTLCache[K, V]) StatsAt(now time.Time) CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purgeExpiredLocked(now)
	stats := c.stats
	stats.Entries = len(c.items)
	stats.Capacity = c.capacity
	stats.TTLMillis = c.ttl.Milliseconds()
	return stats
}

func (c *TTLCache[K, V]) removeLocked(key K) {
	delete(c.items, key)
	for index, candidate := range c.order {
		if candidate == key {
			copy(c.order[index:], c.order[index+1:])
			var zero K
			c.order[len(c.order)-1] = zero
			c.order = c.order[:len(c.order)-1]
			return
		}
	}
}

func (c *TTLCache[K, V]) purgeExpiredLocked(now time.Time) {
	for key, entry := range c.items {
		if !now.Before(entry.expiresAt) {
			delete(c.items, key)
			c.stats.Expirations++
		}
	}
	old := c.order
	kept := old[:0]
	for _, key := range old {
		if _, found := c.items[key]; found {
			kept = append(kept, key)
		}
	}
	var zero K
	for index := len(kept); index < len(old); index++ {
		old[index] = zero
	}
	c.order = kept
}
