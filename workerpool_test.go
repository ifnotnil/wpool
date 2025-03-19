package wpool

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestWorkerPoolLifeCycle(t *testing.T) {
	safeWait := func(initCTX context.Context, d time.Duration) func(context.Context, int) {
		return func(ctx context.Context, _ int) {
			tm := time.NewTicker(d)
			select {
			case <-tm.C:
			case <-ctx.Done():
			case <-initCTX.Done():
			}
		}
	}

	t.Run("noop", func(t *testing.T) {
		ctx := context.Background()
		subject := NewWorkerPool(noop)
		subject.Start(ctx, 10)
		subject.Stop(ctx)
	})

	t.Run("happy path send and read", func(t *testing.T) {
		ctx := context.Background()

		expected := sync.Map{}
		expected.Store(1, nil)
		expected.Store(2, nil)
		expected.Store(3, nil)
		cb := func(ctx context.Context, item int) {
			expected.Delete(item)
		}

		subject := NewWorkerPool(cb)
		subject.Start(ctx, 4)
		err := subject.Submit(ctx, 1)
		require.NoError(t, err)
		err = subject.Submit(ctx, 2)
		require.NoError(t, err)
		err = subject.Submit(ctx, 3)
		require.NoError(t, err)
		subject.Stop(ctx)

		expected.Range(func(key, value any) bool {
			t.Errorf("expected all keys to be deleted: %#v", key)
			return true
		})
	})

	t.Run("noop multiple stops", func(t *testing.T) {
		const testSize = 10
		ctx := context.Background()
		subject := NewWorkerPool(noop)
		subject.Start(ctx, 10)
		var wg sync.WaitGroup
		wg.Add(testSize)
		for range testSize {
			go func() {
				defer wg.Done()
				subject.Stop(ctx)
			}()
		}
		wg.Wait()
	})

	t.Run("buffered channel", func(t *testing.T) {
		ctx := context.Background()
		subject := NewWorkerPool(noop, WithChannelBufferSize(10))
		err := subject.Submit(ctx, 1)
		require.NoError(t, err)
		err = subject.Submit(ctx, 2)
		require.NoError(t, err)
		err = subject.Submit(ctx, 3)
		require.NoError(t, err)
	})

	t.Run("submit fails because of ctx cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		subject := NewWorkerPool(noop)

		// don't start workers to block the submit.

		go func() {
			time.Sleep(200 * time.Millisecond)
			cancel()
		}()

		err := subject.Submit(ctx, 1)
		ErrorStringContains("context canceled")(t, err)
	})

	t.Run("submit fails because pool closes", func(t *testing.T) {
		ctx := context.Background()
		subject := NewWorkerPool(noop)

		// don't start workers to block the submit.
		subject.Start(ctx, 0) // noop start

		go func() {
			time.Sleep(200 * time.Millisecond)
			subject.Stop(ctx)
		}()

		err := subject.Submit(ctx, 1)
		ErrorIs(ErrWorkerPoolStopped)(t, err)
	})

	t.Run("send to closed pool", func(t *testing.T) {
		ctx := context.Background()
		subject := NewWorkerPool(noop)
		subject.Start(ctx, 10)
		subject.Stop(ctx)
		err := subject.Submit(ctx, 1)
		ErrorIs(ErrWorkerPoolStopped)(t, err)
	})

	t.Run("close while 20 senders submitting", func(t *testing.T) {
		ctx := context.Background()
		ctx2, ctx2Cancel := context.WithCancel(ctx)
		cb := safeWait(ctx2, 30*time.Millisecond)
		subject := NewWorkerPool(cb, WithChannelBufferSize(0))
		subject.Start(ctx, 2)

		failedSubmits := atomic.Int64{}
		const senders = 20
		wg := sync.WaitGroup{}
		wg.Add(senders)
		for range senders {
			go func() {
				defer wg.Done()
				for i := range 400 {
					err := subject.Submit(ctx, i)
					if err != nil {
						ErrorIs(ErrWorkerPoolStopped)(t, err)
						failedSubmits.Add(1)
					}
				}
			}()
		}

		ctx2Cancel()
		subject.Stop(ctx)
		wg.Wait()
		require.Positive(t, failedSubmits.Load(), "at least one submit should fail")
	})

	t.Run("close with ctx cancel while sending", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cb := func(_ context.Context, it int) { time.Sleep(60 * time.Millisecond) }
		subject := NewWorkerPool(cb)
		subject.Start(ctx, 10)

		wg := sync.WaitGroup{}
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			for i := range 100 {
				err := subject.Submit(ctx, i)
				if err != nil {
					ErrorIs(ErrWorkerPoolStopped)(t, err)
				}
			}
		}(context.Background())
		cancel()

		wg.Wait()
		subject.Stop(ctx)
	})

	t.Run("close with ctx cancel before sending", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cb := func(_ context.Context, it int) { time.Sleep(60 * time.Millisecond) }
		subject := NewWorkerPool(cb)
		cancel()
		err := subject.Submit(ctx, 1)
		ErrorStringContains("worker pool item submission failed due to context cancellation")(t, err)
	})

	t.Run("noop with logs", func(t *testing.T) {
		ctx := context.Background()
		subject := NewWorkerPool(noop, WithLogger(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))))
		subject.Start(ctx, 10)
		subject.Stop(ctx)
	})

	t.Run("concurrent submits and close", func(t *testing.T) {
		for _, testSize := range []int{1, 10, 100, 1000} {
			t.Run(fmt.Sprintf("testSize=%d", testSize), func(t *testing.T) {
				for _, bufferSize := range []int{0, 1, 10, 100} {
					t.Run(fmt.Sprintf("buffer=%d", bufferSize), func(t *testing.T) {
						testConcurrentSubmitsAndClose(t, testSize, bufferSize)
					})
				}
			})
		}
	})
}

func testConcurrentSubmitsAndClose(t *testing.T, testSize, bufferSize int) {
	t.Helper()
	ctx := context.Background()
	seenSum := atomic.Int64{}
	subject := NewWorkerPool(
		func(ctx context.Context, item int) {
			seenSum.Add(int64(item))
		},
		WithChannelBufferSize(bufferSize),
	)
	subject.Start(ctx, 10)
	sentSum := atomic.Int64{}
	var wg sync.WaitGroup
	wg.Add(testSize + 1)
	for i := range testSize {
		if i == testSize/10 {
			go func() {
				defer wg.Done()
				subject.Stop(ctx)
			}()
		}

		go func() {
			defer wg.Done()
			err := subject.Submit(ctx, i)
			if err == nil {
				sentSum.Add(int64(i))
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, sentSum.Load(), seenSum.Load())
}

func TestMultipleSenders(t *testing.T) {
	ctx := context.Background()

	const senders = 40
	const perSender = 4000
	count := &atomic.Int64{}

	cb := func(_ context.Context, _ int) { count.Add(1) }
	subject := NewWorkerPool(cb, WithChannelBufferSize(3))
	subject.Start(ctx, 5)

	wg := sync.WaitGroup{}
	wg.Add(senders)
	startSignal := make(chan struct{})
	for range senders {
		go func() {
			defer wg.Done()
			<-startSignal
			for i := range perSender {
				err := subject.Submit(ctx, i)
				assert.NoError(t, err)
			}
		}()
	}
	close(startSignal)
	wg.Wait()

	subject.Stop(ctx)

	assert.Equal(t, int64(senders*perSender), count.Load())
}

func BenchmarkNew(b *testing.B) {
	for range b.N {
		NewWorkerPool(noop)
	}
}

func BenchmarkSubmit(b *testing.B) {
	ctx := context.Background()

	subject := NewWorkerPool(noop, WithChannelBufferSize(b.N+1))

	b.ResetTimer()
	for i := range b.N {
		_ = subject.Submit(ctx, i)
	}
	b.StopTimer()

	subject.Stop(ctx)
}

func BenchmarkStop(b *testing.B) {
	ctx := context.Background()
	for range b.N {
		NewWorkerPool(noop).Stop(ctx)
	}
}

func BenchmarkWork(b *testing.B) {
	ctx := context.Background()

	subject := NewWorkerPool(noop, WithChannelBufferSize(b.N+1))

	for i := range b.N {
		_ = subject.Submit(ctx, i)
	}
	close(subject.ch)

	subject.workersWG.Add(1)
	b.ResetTimer()
	subject.worker(ctx, 0)
	b.StopTimer()
}

func BenchmarkFullFlow(b *testing.B) {
	tests := []struct {
		workers           int
		senders           int
		channelBufferSize int
	}{
		{
			workers:           10,
			senders:           10,
			channelBufferSize: 10,
		},
		{
			workers:           20,
			senders:           20,
			channelBufferSize: 10,
		},
		{
			workers:           10,
			senders:           20,
			channelBufferSize: 10,
		},
		{
			workers:           10,
			senders:           40,
			channelBufferSize: 10,
		},
		{
			workers:           10,
			senders:           40,
			channelBufferSize: 50,
		},
	}

	for idx, tc := range tests {
		b.Run(fmt.Sprintf("%d_w%d_s%d_b%d", idx, tc.workers, tc.senders, tc.channelBufferSize), func(b *testing.B) {
			ctx := context.Background()

			subject := NewWorkerPool(noop, WithChannelBufferSize(tc.channelBufferSize))

			start := make(chan struct{})

			wg := sync.WaitGroup{}
			wg.Add(tc.senders)

			for range tc.senders {
				go mockSender(b, ctx, &wg, start, subject)
			}
			close(start)

			subject.Start(ctx, tc.workers)

			wg.Wait()
			subject.Stop(ctx)
		})
	}
}

func noop(context.Context, int) {}

func mockSender(b *testing.B, ctx context.Context, wg *sync.WaitGroup, start chan struct{}, subject *WorkerPool[int]) {
	b.Helper()
	defer wg.Done()
	<-start
	for i := range b.N {
		_ = subject.Submit(ctx, i)
	}
}
