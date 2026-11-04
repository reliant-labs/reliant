package middleware

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestDedupeStore_SeenAfterMark(t *testing.T) {
	s := NewDedupeStore(DedupeConfig{MaxKeys: 10, TTL: time.Minute})
	if s.Seen("evt-1") {
		t.Fatal("unmarked key reported as seen")
	}
	s.Mark("evt-1")
	if !s.Seen("evt-1") {
		t.Fatal("marked key reported as unseen")
	}
	if s.Seen("evt-2") {
		t.Fatal("different key reported as seen")
	}
}

func TestDedupeStore_EmptyKey(t *testing.T) {
	s := NewDedupeStore(DedupeConfig{})
	s.Mark("")
	if s.Seen("") {
		t.Fatal("empty key must never be seen — the caller has to fall through to processing")
	}
	if got := s.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0 (empty key must not consume a slot)", got)
	}
}

func TestDedupeStore_TTLExpiry(t *testing.T) {
	s := NewDedupeStore(DedupeConfig{MaxKeys: 10, TTL: time.Millisecond})
	s.Mark("evt-1")
	time.Sleep(5 * time.Millisecond)
	if s.Seen("evt-1") {
		t.Fatal("key still seen after TTL lapsed")
	}
}

// TestDedupeStore_Bounded is the whole point of the type: the scaffolded
// map+mutex it replaces grew for the life of the process.
func TestDedupeStore_Bounded(t *testing.T) {
	s := NewDedupeStore(DedupeConfig{MaxKeys: 4, TTL: time.Hour})
	for i := 0; i < 1000; i++ {
		s.Mark(fmt.Sprintf("evt-%d", i))
	}
	if got := s.Len(); got != 4 {
		t.Fatalf("Len() = %d, want 4 (MaxKeys bound)", got)
	}
	if !s.Seen("evt-999") {
		t.Fatal("most recent key evicted")
	}
	if s.Seen("evt-0") {
		t.Fatal("oldest key survived eviction")
	}
}

func TestDedupeStore_Defaults(t *testing.T) {
	s := NewDedupeStore(DedupeConfig{})
	if s.ttl != time.Hour {
		t.Fatalf("default TTL = %v, want 1h", s.ttl)
	}
	if s.cache.max != 1000 {
		t.Fatalf("default MaxKeys = %d, want 1000", s.cache.max)
	}
}

func TestDedupeStore_ConcurrentUse(t *testing.T) {
	s := NewDedupeStore(DedupeConfig{MaxKeys: 256, TTL: time.Minute})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := fmt.Sprintf("evt-%d-%d", i, j)
				_ = s.Seen(key)
				s.Mark(key)
			}
		}(i)
	}
	wg.Wait()
	if got := s.Len(); got > 256 {
		t.Fatalf("Len() = %d, want <= 256", got)
	}
}
