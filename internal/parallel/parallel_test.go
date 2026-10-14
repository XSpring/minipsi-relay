package parallel

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestForCoversRangeExactlyOnce(t *testing.T) {
	for _, n := range []int{0, 1, 7, 100, 10007} {
		for _, workers := range []int{1, 2, 8} {
			for _, grain := range []int{0, 1, 64} {
				hits := make([]int32, n)
				err := For(context.Background(), n, workers, grain, func(lo, hi int) error {
					if lo >= hi {
						t.Errorf("empty chunk [%d,%d)", lo, hi)
					}
					for i := lo; i < hi; i++ {
						atomic.AddInt32(&hits[i], 1)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				for i, h := range hits {
					if h != 1 {
						t.Fatalf("n=%d w=%d g=%d: index %d hit %d times", n, workers, grain, i, h)
					}
				}
			}
		}
	}
}

func TestForPropagatesError(t *testing.T) {
	boom := errors.New("boom")
	for _, workers := range []int{1, 4} {
		err := For(context.Background(), 1000, workers, 1, func(lo, hi int) error {
			if lo <= 500 && 500 < hi {
				return boom
			}
			return nil
		})
		if !errors.Is(err, boom) {
			t.Fatalf("workers=%d: got %v", workers, err)
		}
	}
}

func TestForCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, workers := range []int{1, 4} {
		var calls int32
		err := For(ctx, 1000, workers, 1, func(lo, hi int) error {
			atomic.AddInt32(&calls, 1)
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("workers=%d: got %v", workers, err)
		}
	}
	if err := For(ctx, 0, 1, 1, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("n=0: got %v", err)
	}
}

func TestWorkers(t *testing.T) {
	if Workers(3) != 3 || Workers(0) < 1 || Workers(-1) < 1 {
		t.Fatal("Workers normalisation wrong")
	}
}
