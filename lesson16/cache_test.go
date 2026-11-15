package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCacheGenericTypesAndValueOwnership(t *testing.T) {
	now := time.Now()
	users, err := NewTTLCache[string, User](2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	users.PutAt("alice", User{ID: 1, Name: "Alice", Points: 100}, now)
	value, found := users.GetAt("alice", now)
	if !found {
		t.Fatal("missing cached user")
	}
	value.Name = "Changed"
	value.Points = 999
	original, _ := users.GetAt("alice", now)
	if original.Name != "Alice" || original.Points != 100 {
		t.Fatal("returned value changed stored user")
	}
	labels, _ := NewTTLCache[int64, string](2, time.Second)
	labels.PutAt(7, "seven", now)
	if value, ok := labels.GetAt(7, now); !ok || value != "seven" {
		t.Fatal("another generic instantiation failed")
	}
}

func TestCacheTTLBoundaryAndOverwriteDoesNotSlideOnRead(t *testing.T) {
	base := time.Now()
	cache, _ := NewTTLCache[string, int](2, 10*time.Second)
	cache.PutAt("a", 1, base)
	if _, ok := cache.GetAt("a", base.Add(9*time.Second)); !ok {
		t.Fatal("expired too early")
	}
	if _, ok := cache.GetAt("a", base.Add(10*time.Second)); ok {
		t.Fatal("read refreshed TTL or equality did not expire")
	}
	cache.PutAt("a", 2, base.Add(11*time.Second))
	cache.PutAt("a", 3, base.Add(16*time.Second))
	if value, ok := cache.GetAt("a", base.Add(25*time.Second)); !ok || value != 3 {
		t.Fatal("overwrite did not reset deadline")
	}
	if _, ok := cache.GetAt("a", base.Add(26*time.Second)); ok {
		t.Fatal("overwritten entry did not expire")
	}
}

func TestCacheFIFOAndExpiredEntriesBeforeEviction(t *testing.T) {
	base := time.Now()
	cache, _ := NewTTLCache[string, int](2, 10*time.Second)
	cache.PutAt("a", 1, base)
	cache.PutAt("b", 2, base.Add(time.Second))
	cache.GetAt("a", base.Add(2*time.Second))
	cache.PutAt("c", 3, base.Add(3*time.Second))
	if _, ok := cache.GetAt("a", base.Add(3*time.Second)); ok {
		t.Fatal("FIFO was changed by a read")
	}
	if _, ok := cache.GetAt("b", base.Add(3*time.Second)); !ok {
		t.Fatal("wrong entry evicted")
	}
	cache.PutAt("b", 20, base.Add(4*time.Second))
	if stats := cache.StatsAt(base.Add(4 * time.Second)); stats.Entries != 2 || stats.Evictions != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	fresh, _ := NewTTLCache[string, int](2, 10*time.Second)
	fresh.PutAt("expired", 1, base)
	fresh.PutAt("live", 2, base.Add(5*time.Second))
	fresh.PutAt("new", 3, base.Add(11*time.Second))
	stats := fresh.StatsAt(base.Add(11 * time.Second))
	if stats.Entries != 2 || stats.Expirations != 1 || stats.Evictions != 0 {
		t.Fatalf("expired entries caused a live eviction: %+v", stats)
	}
}

func TestCacheDeleteAndReplacementMetadataStayBounded(t *testing.T) {
	base := time.Now()
	cache, _ := NewTTLCache[int, int](2, time.Hour)
	for index := 0; index < 2000; index++ {
		cache.PutAt(index%3, index, base)
	}
	cache.mu.Lock()
	entries, queued := len(cache.items), len(cache.order)
	cache.mu.Unlock()
	if entries > 2 || queued != entries {
		t.Fatalf("unbounded metadata: entries=%d queued=%d", entries, queued)
	}
	for key := 0; key < 3; key++ {
		cache.Delete(key)
		cache.Delete(key)
	}
	if stats := cache.StatsAt(base); stats.Entries != 0 {
		t.Fatal("delete retained entries")
	}
}

func TestCacheRejectsInvalidConfiguration(t *testing.T) {
	for _, args := range []struct {
		capacity int
		ttl      time.Duration
	}{{0, time.Second}, {-1, time.Second}, {1, 0}, {1, -time.Second}} {
		if _, err := NewTTLCache[int, string](args.capacity, args.ttl); err == nil {
			t.Fatalf("accepted config=%+v", args)
		}
	}
}

func TestCacheConcurrentAccessStaysBounded(t *testing.T) {
	base := time.Now()
	cache, _ := NewTTLCache[int, int](8, time.Hour)
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for index := 0; index < 128; index++ {
				key := (worker + index) % 16
				cache.PutAt(key, index, base)
				cache.GetAt(key, base)
				if index%3 == 0 {
					cache.Delete(key)
				}
				if stats := cache.StatsAt(base); stats.Entries > 8 {
					t.Errorf("capacity exceeded: %+v", stats)
				}
			}
		}(worker)
	}
	workers.Wait()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.order) != len(cache.items) || len(cache.items) > 8 {
		t.Fatal("concurrent operations corrupted cache bounds")
	}
}

func TestReceiptCacheHTTPHitExpiryDeleteAndSnapshot(t *testing.T) {
	repo, _ := newTransferRepository(t, 100, 20)
	first, err := repo.Transfer(context.Background(), "first", 1, 2, 30)
	if err != nil {
		t.Fatal(err)
	}
	cache, _ := NewTTLCache[int64, TransferReceipt](2, time.Second)
	handler := NewHandlerWithCache(repo, cache)
	path := fmt.Sprintf("/transfers/%d", first.Transfer.ID)
	miss := request(handler, "GET", path, "", "")
	hit := request(handler, "GET", path, "", "")
	if miss.Code != 200 || miss.Header().Get("X-Receipt-Cache") != "MISS" || hit.Header().Get("X-Receipt-Cache") != "HIT" || hit.Body.String() != miss.Body.String() {
		t.Fatal("cache hit/miss protocol failed")
	}
	if _, err := repo.Transfer(context.Background(), "second", 1, 2, 10); err != nil {
		t.Fatal(err)
	}
	stillSnapshot := request(handler, "GET", path, "", "")
	current := request(handler, "GET", "/users/1", "", "")
	var user User
	if stillSnapshot.Header().Get("X-Receipt-Cache") != "HIT" || stillSnapshot.Body.String() != miss.Body.String() || json.Unmarshal(current.Body.Bytes(), &user) != nil || user.Points != 60 || user.Version != 2 || current.Header().Get("X-Receipt-Cache") != "" {
		t.Fatal("cached receipt was confused with current balance")
	}
	deleted := request(handler, "DELETE", fmt.Sprintf("/cache/receipts/%d", first.Transfer.ID), "", "")
	if deleted.Code != 204 {
		t.Fatal("cache deletion failed")
	}
	if response := request(handler, "GET", path, "", ""); response.Header().Get("X-Receipt-Cache") != "MISS" {
		t.Fatal("manual delete did not evict")
	}
	cache.PutAt(first.Transfer.ID, first.Transfer, time.Now().Add(-2*time.Second))
	if response := request(handler, "GET", path, "", ""); response.Header().Get("X-Receipt-Cache") != "MISS" {
		t.Fatal("expired entry was returned")
	}
	statsResponse := request(handler, "GET", "/cache/stats", "", "")
	var stats struct {
		CacheStats
		DatabaseLoads uint64 `json:"database_loads"`
	}
	if json.Unmarshal(statsResponse.Body.Bytes(), &stats) != nil || stats.DatabaseLoads != 3 || stats.Hits != 2 || stats.Expirations != 1 {
		t.Fatalf("stats=%s", statsResponse.Body)
	}
}

func TestReceiptCacheDoesNotCacheNotFound(t *testing.T) {
	repo, _ := newTransferRepository(t, 100, 20)
	handler := NewHandler(repo)
	for index := 0; index < 2; index++ {
		if response := request(handler, "GET", "/transfers/1", "", ""); response.Code != 404 {
			t.Fatal(response.Body)
		}
	}
	if _, err := repo.Transfer(context.Background(), "created-after-miss", 1, 2, 30); err != nil {
		t.Fatal(err)
	}
	if response := request(handler, "GET", "/transfers/1", "", ""); response.Code != 200 || response.Header().Get("X-Receipt-Cache") != "MISS" {
		t.Fatal("prior 404 hid the new receipt")
	}
	statsResponse := request(handler, "GET", "/cache/stats", "", "")
	var stats struct {
		DatabaseLoads uint64 `json:"database_loads"`
		Entries       int    `json:"entries"`
	}
	if json.Unmarshal(statsResponse.Body.Bytes(), &stats) != nil || stats.DatabaseLoads != 3 || stats.Entries != 1 {
		t.Fatalf("stats=%s", statsResponse.Body)
	}
}

func TestReceiptCacheMutexIsNotHeldWhileDatabaseWaits(t *testing.T) {
	repo, _ := newTransferRepository(t, 100, 20)
	if _, err := repo.Transfer(context.Background(), "one", 1, 2, 30); err != nil {
		t.Fatal(err)
	}
	repo.db.SetMaxOpenConns(1)
	connection, err := repo.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	handler := NewHandler(repo)
	finished := make(chan int, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		req := httptest.NewRequest("GET", "/transfers/1", nil).WithContext(ctx)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		finished <- response.Code
	}()
	deadline := time.Now().Add(time.Second)
	observed := false
	for time.Now().Before(deadline) {
		statsDone := make(chan uint64, 1)
		go func() {
			response := request(handler, "GET", "/cache/stats", "", "")
			var stats struct {
				DatabaseLoads uint64 `json:"database_loads"`
			}
			_ = json.Unmarshal(response.Body.Bytes(), &stats)
			statsDone <- stats.DatabaseLoads
		}()
		select {
		case loads := <-statsDone:
			if loads == 1 {
				observed = true
			}
		case <-time.After(time.Second):
			t.Fatal("cache mutex blocked stats while database pool was busy")
		}
		if observed {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !observed {
		t.Fatal("database lookup was not started")
	}
	connection.Close()
	select {
	case status := <-finished:
		if status != 200 {
			t.Fatalf("status=%d", status)
		}
	case <-ctx.Done():
		t.Fatal("database lookup did not resume")
	}
}
