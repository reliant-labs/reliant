package middleware

import (
	"testing"
	"time"
)

func TestIdempotencyInterceptor_DisabledWhenCacheSizeZero(t *testing.T) {
	t.Parallel()
	if got := IdempotencyInterceptor(IdempotencyOptions{CacheSize: 0}); got != nil {
		t.Fatal("expected nil interceptor when CacheSize <= 0")
	}
	if got := IdempotencyInterceptor(IdempotencyOptions{CacheSize: -1}); got != nil {
		t.Fatal("expected nil interceptor when CacheSize < 0")
	}
}

func TestIdempotencyInterceptor_DefaultTTL(t *testing.T) {
	t.Parallel()
	ic := IdempotencyInterceptor(IdempotencyOptions{CacheSize: 10})
	if ic == nil {
		t.Fatal("expected non-nil interceptor")
	}
	ii := ic.(*idempotencyInterceptor)
	if ii.ttl != time.Hour {
		t.Fatalf("expected default TTL of 1h, got %v", ii.ttl)
	}
}

func TestIdempotencyInterceptor_CustomTTL(t *testing.T) {
	t.Parallel()
	ic := IdempotencyInterceptor(IdempotencyOptions{CacheSize: 10, TTL: 5 * time.Minute})
	ii := ic.(*idempotencyInterceptor)
	if ii.ttl != 5*time.Minute {
		t.Fatalf("expected TTL 5m, got %v", ii.ttl)
	}
}

func TestIdempotencyCacheKey_ScopesByProcedure(t *testing.T) {
	t.Parallel()
	k1 := idempotencyCacheKey("/svc.v1.Foo/Create", "abc")
	k2 := idempotencyCacheKey("/svc.v1.Bar/Create", "abc")
	if k1 == k2 {
		t.Fatal("same client key on different procedures must produce different cache keys")
	}
}

func TestIdempotencyCacheKey_SameInputsSameOutput(t *testing.T) {
	t.Parallel()
	k1 := idempotencyCacheKey("/svc.v1.Foo/Create", "key-1")
	k2 := idempotencyCacheKey("/svc.v1.Foo/Create", "key-1")
	if k1 != k2 {
		t.Fatal("same procedure + client key must produce identical cache keys")
	}
}

func TestIdempotencyCacheKey_DifferentKeys(t *testing.T) {
	t.Parallel()
	k1 := idempotencyCacheKey("/svc.v1.Foo/Create", "key-1")
	k2 := idempotencyCacheKey("/svc.v1.Foo/Create", "key-2")
	if k1 == k2 {
		t.Fatal("different client keys must produce different cache keys")
	}
}

func TestIdempotencyCache_StoresAndReturns(t *testing.T) {
	t.Parallel()
	ic := IdempotencyInterceptor(IdempotencyOptions{CacheSize: 100, TTL: time.Minute})
	ii := ic.(*idempotencyInterceptor)

	key := idempotencyCacheKey("/svc/Method", "req-1")

	// Store an entry.
	ii.mu.Lock()
	ii.cache.add(key, &cachedRPCResponse{
		resp:      nil,
		err:       nil,
		expiresAt: time.Now().Add(time.Minute),
	})
	ii.mu.Unlock()

	// Should find it.
	ii.mu.Lock()
	cached, ok := ii.cache.get(key)
	ii.mu.Unlock()
	if !ok {
		t.Fatal("expected cache hit")
	}
	if time.Now().After(cached.expiresAt) {
		t.Fatal("entry should not be expired")
	}
}

func TestIdempotencyCache_ExpiredEntryIgnored(t *testing.T) {
	t.Parallel()
	ic := IdempotencyInterceptor(IdempotencyOptions{CacheSize: 100, TTL: time.Minute})
	ii := ic.(*idempotencyInterceptor)

	key := idempotencyCacheKey("/svc/Method", "req-2")

	// Store an already-expired entry.
	ii.mu.Lock()
	ii.cache.add(key, &cachedRPCResponse{
		resp:      nil,
		err:       nil,
		expiresAt: time.Now().Add(-time.Second),
	})
	ii.mu.Unlock()

	// The entry exists in LRU but is logically expired.
	ii.mu.Lock()
	cached, ok := ii.cache.get(key)
	ii.mu.Unlock()
	if !ok {
		t.Fatal("expected cache to contain the key")
	}
	if time.Now().Before(cached.expiresAt) {
		t.Fatal("entry should be expired")
	}
}
