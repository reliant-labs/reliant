package observe

import (
	"math"
	"sync"
	"testing"
	"time"
)

// BenchmarkOpStatsObserve measures the slow estimate alone, uncontended and
// with every CPU folding samples into one op.
func BenchmarkOpStatsObserve(b *testing.B) {
	p := newSpanPolicy(nil)
	b.Run("serial", func(b *testing.B) {
		var s opStats
		b.ReportAllocs()
		for b.Loop() {
			s.observe(10*time.Millisecond, p)
		}
	})
	b.Run("parallel", func(b *testing.B) {
		var s opStats
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				s.observe(10*time.Millisecond, p)
			}
		})
	})
}

// TestOpStatsConcurrentSamplesAreNeverLost: while an op has fewer samples
// than its window its normal is the exact running mean of ln(duration), which
// does not depend on arrival order. So 32 goroutines each folding in one
// distinct duration at once must land on exactly that mean, with a count of
// 32. A load-then-store update that let a stale writer overwrite a newer mean
// would drop samples and miss it.
func TestOpStatsConcurrentSamplesAreNeverLost(t *testing.T) {
	p := newSpanPolicy(nil)
	const writers = 32
	want := 0.0
	for i := range writers {
		want += math.Log(float64(time.Duration(i+1) * time.Millisecond))
	}
	want /= writers
	for trial := range 50 {
		var s opStats
		var start, done sync.WaitGroup
		start.Add(1)
		for i := range writers {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				s.observe(time.Duration(i+1)*time.Millisecond, p)
			}()
		}
		start.Done()
		done.Wait()
		mean, n := s.snapshot()
		if n != writers {
			t.Fatalf("trial %d: count = %d, want %d", trial, n, writers)
		}
		if math.Abs(mean-want) > 1e-4 {
			t.Fatalf("trial %d: mean ln(ns) = %.6f, want %.6f: a concurrent sample was lost", trial, mean, want)
		}
	}
}

// TestOpStatsConvergesUnderContention: many goroutines feeding known
// durations (8ms and 12ms, alternating) keep every sample and converge to a
// normal between them, near their geometric mean.
func TestOpStatsConvergesUnderContention(t *testing.T) {
	p := newSpanPolicy(nil)
	const writers, each = 16, 2000
	var s opStats
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range each {
				d := 8 * time.Millisecond
				if j%2 == 1 {
					d = 12 * time.Millisecond
				}
				s.observe(d, p)
			}
		}()
	}
	wg.Wait()
	mean, n := s.snapshot()
	if n != writers*each {
		t.Fatalf("count = %d, want %d: samples were lost", n, writers*each)
	}
	normal := time.Duration(math.Exp(mean))
	if normal < 8*time.Millisecond || normal > 12*time.Millisecond {
		t.Fatalf("normal = %v, want between 8ms and 12ms (about 9.8ms)", normal)
	}
}
