// Package parallel provides bounded, cancellable parallel loops.
package parallel

import (
	"context"
	"runtime"
	"sync"
)

// Workers normalises a worker count: n <= 0 means GOMAXPROCS.
func Workers(n int) int {
	if n <= 0 {
		return runtime.GOMAXPROCS(0)
	}
	return n
}

// For splits [0, n) into contiguous chunks of at least grain elements and runs
// fn(lo, hi) on up to workers goroutines. It stops scheduling new chunks after the first
// error or context cancellation and returns that error.
func For(ctx context.Context, n, workers, grain int, fn func(lo, hi int) error) error {
	if n <= 0 {
		return ctx.Err()
	}
	if grain < 1 {
		grain = 1
	}
	workers = Workers(workers)
	// Aim for ~4 chunks per worker for load balancing, but never below grain.
	chunk := (n + 4*workers - 1) / (4 * workers)
	if chunk < grain {
		chunk = grain
	}
	chunks := (n + chunk - 1) / chunk
	if workers > chunks {
		workers = chunks
	}
	if workers == 1 {
		for lo := 0; lo < n; lo += chunk {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(lo, min(lo+chunk, n)); err != nil {
				return err
			}
		}
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	next := make(chan int)
	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	fail := func(err error) {
		once.Do(func() { firstErr = err; cancel() })
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for lo := range next {
				if err := fn(lo, min(lo+chunk, n)); err != nil {
					fail(err)
					return
				}
			}
		}()
	}
feed:
	for lo := 0; lo < n; lo += chunk {
		select {
		case next <- lo:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
