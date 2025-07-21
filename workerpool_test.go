package wpool

import (
	"bytes"
	"context"
	"fmt"
	"iter"
	"log/slog"
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
	t.Run("noop", func(t *testing.T) {
		ctx := context.Background()
		subject := NewWorkerPool(noop)
		subject.Start(ctx, 10)
		subject.Stop(ctx)
	})

	t.Run("noop - immediate", func(t *testing.T) {
		ctx := context.Background()
		subject := NewWorkerPool(noop, WithShutdownMode(ShutdownModeImmediate))
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

	t.Run("happy path send and read - immediate", func(t *testing.T) {
		ctx := context.Background()

		expected := sync.Map{}

		// slow read
		cb := func(ctx context.Context, item int) {
			time.Sleep(10 * time.Millisecond)
			expected.Delete(item)
		}

		subject := NewWorkerPool(cb, WithChannelBufferSize(100), WithShutdownMode(ShutdownModeImmediate))
		subject.Start(ctx, 1)

		// set expected sends
		for i := range 100 {
			expected.Store(i, nil)
		}

		// send
		for i := range 100 {
			err := subject.Submit(ctx, i)
			require.NoError(t, err)
		}

		// stop
		subject.Stop(ctx)

		// expect at least some not processed due to immediate shutdown.
		var count int
		expected.Range(func(key, value any) bool {
			count++
			return true
		})
		require.Positive(t, count)
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

	t.Run("submit fails because of ctx cancellation - immediate", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		subject := NewWorkerPool(noop, WithShutdownMode(ShutdownModeImmediate))

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

	t.Run("submit fails because pool closes - immediate", func(t *testing.T) {
		ctx := context.Background()
		subject := NewWorkerPool(noop, WithShutdownMode(ShutdownModeImmediate))

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

	t.Run("send to closed pool - immediate", func(t *testing.T) {
		ctx := context.Background()
		subject := NewWorkerPool(noop, WithShutdownMode(ShutdownModeImmediate))
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

	t.Run("close with ctx cancel before sending - immediate", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cb := func(_ context.Context, it int) { time.Sleep(60 * time.Millisecond) }
		subject := NewWorkerPool(cb, WithShutdownMode(ShutdownModeImmediate))
		cancel()
		err := subject.Submit(ctx, 1)
		ErrorStringContains("worker pool item submission failed due to context cancellation")(t, err)
	})

	t.Run("noop with logs", func(t *testing.T) {
		ctx := context.Background()
		buf := bytes.Buffer{}
		subject := NewWorkerPool(noop, WithLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
		subject.Start(ctx, 10)
		subject.Stop(ctx)
		require.NotZero(t, buf.Len())
	})
}

func TestConcurrentSubmits(t *testing.T) {
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

type WorkerPoolTestCaseMidFlight struct {
	enabled     bool
	onSenderID  int
	onSendCount int
	fn          func(ctx context.Context, ctxCancelFn func(), subject *WorkerPool[int])
}

type WorkerPoolTestCase struct {
	opts                []func(*config)
	callback            func(ctx context.Context, item int)
	workers             int
	senders             int
	sendsPerSender      int // < 0 means infinite sends
	stop                bool
	midFlight           WorkerPoolTestCaseMidFlight
	assertErrorOnSubmit func(t *testing.T, err error)
	asserts             func(t *testing.T, itemsSent uint64, itemsProcessed uint64)
}

func (tc WorkerPoolTestCase) Test(t *testing.T) {
	ctx, ctxCancel := context.WithCancel(context.Background())
	defer ctxCancel()

	countSent := &atomic.Uint64{}
	countProcessed := &atomic.Uint64{}

	cb := func(ctx context.Context, i int) {
		countProcessed.Add(1)
		tc.callback(ctx, i)
	}

	subject := NewWorkerPool(cb, tc.opts...)
	subject.Start(ctx, tc.workers)

	sendersWG := sync.WaitGroup{}
	sendersWG.Add(tc.senders)
	startSignal := make(chan struct{})
	for sid := range tc.senders {
		go func(senderID int) {
			defer sendersWG.Done()
			<-startSignal
			for i := range intIter(tc.sendsPerSender) {
				err := subject.Submit(ctx, i)
				if tc.assertErrorOnSubmit != nil {
					tc.assertErrorOnSubmit(t, err)
				}
				if err != nil {
					break
				}

				countSent.Add(1)

				if tc.midFlight.enabled &&
					tc.midFlight.fn != nil &&
					tc.midFlight.onSenderID == senderID &&
					tc.midFlight.onSendCount == i {
					tc.midFlight.fn(ctx, ctxCancel, subject)
				}
			}
		}(sid)
	}
	close(startSignal)
	sendersWG.Wait()

	if tc.stop {
		subject.Stop(ctx)
	}

	if tc.asserts != nil {
		tc.asserts(t, countSent.Load(), countProcessed.Load())
	}
}

//nolint:thelper
func TestFlow(t *testing.T) {
	tests := map[string]WorkerPoolTestCase{
		"1 worker 1 sender 100": {
			opts:           []func(*config){},
			callback:       noop,
			workers:        1,
			senders:        1,
			sendsPerSender: 100,
			stop:           true,
			midFlight:      WorkerPoolTestCaseMidFlight{},
			assertErrorOnSubmit: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
			asserts: func(t *testing.T, itemsSent, itemsProcessed uint64) {
				assert.Equal(t, uint64(100), itemsSent)
				assert.Equal(t, uint64(100), itemsProcessed)
			},
		},
		"5 workers 10 sender 40k": {
			opts:           []func(*config){},
			callback:       noop,
			workers:        5,
			senders:        10,
			sendsPerSender: 40_000,
			stop:           true,
			midFlight:      WorkerPoolTestCaseMidFlight{},
			assertErrorOnSubmit: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
			asserts: func(t *testing.T, itemsSent, itemsProcessed uint64) {
				assert.Equal(t, uint64(10*40_000), itemsSent)
				assert.Equal(t, uint64(10*40_000), itemsProcessed)
			},
		},
		"5 workers 10 sender 80k": {
			opts:           []func(*config){},
			callback:       noop,
			workers:        5,
			senders:        10,
			sendsPerSender: 80_000,
			stop:           true,
			midFlight:      WorkerPoolTestCaseMidFlight{},
			assertErrorOnSubmit: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
			asserts: func(t *testing.T, itemsSent, itemsProcessed uint64) {
				assert.Equal(t, uint64(10*80_000), itemsSent)
				assert.Equal(t, uint64(10*80_000), itemsProcessed)
			},
		},
		"5 workers 40 sender 40k": {
			opts:           []func(*config){},
			callback:       noop,
			workers:        5,
			senders:        40,
			sendsPerSender: 40_000,
			stop:           true,
			midFlight:      WorkerPoolTestCaseMidFlight{},
			assertErrorOnSubmit: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
			asserts: func(t *testing.T, itemsSent, itemsProcessed uint64) {
				assert.Equal(t, uint64(40*40_000), itemsSent)
				assert.Equal(t, uint64(40*40_000), itemsProcessed)
			},
		},
		"5 workers 5 sender": {
			opts:           []func(*config){WithChannelBufferSize(100)},
			callback:       noop,
			workers:        5,
			senders:        5,
			sendsPerSender: -1,
			stop:           false,
			midFlight: WorkerPoolTestCaseMidFlight{
				enabled:     true,
				onSenderID:  2,
				onSendCount: 15000,
				fn: func(ctx context.Context, ctxCancelFn func(), subject *WorkerPool[int]) {
					subject.Stop(ctx)
				},
			},
			assertErrorOnSubmit: func(t *testing.T, err error) {
				if err != nil {
					require.ErrorIs(t, err, ErrWorkerPoolStopped)
				}
			},
			asserts: func(t *testing.T, itemsSent, itemsProcessed uint64) {
				assert.Equal(t, itemsSent, itemsProcessed)
			},
		},
		"5 workers 5 sender ShutdownModeImmediate": {
			opts:           []func(*config){WithChannelBufferSize(100), WithShutdownMode(ShutdownModeImmediate)},
			callback:       noop,
			workers:        5,
			senders:        5,
			sendsPerSender: -1,
			stop:           false,
			midFlight: WorkerPoolTestCaseMidFlight{
				enabled:     true,
				onSenderID:  2,
				onSendCount: 15000,
				fn: func(ctx context.Context, ctxCancelFn func(), subject *WorkerPool[int]) {
					subject.Stop(ctx)
				},
			},
			assertErrorOnSubmit: func(t *testing.T, err error) {
				if err != nil {
					require.ErrorIs(t, err, ErrWorkerPoolStopped)
				}
			},
			asserts: func(t *testing.T, itemsSent, itemsProcessed uint64) {
				assert.Less(t, itemsProcessed, itemsSent)
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, tc.Test)
	}
}

// helper functions

func noop(context.Context, int) {}

var safeWait = func(initCTX context.Context, d time.Duration) func(context.Context, int) {
	return func(ctx context.Context, _ int) {
		tm := time.NewTicker(d)
		select {
		case <-tm.C:
		case <-ctx.Done():
		case <-initCTX.Done():
		}
	}
}

func intIter(end int) iter.Seq[int] {
	return func(yield func(int) bool) {
		if end > 0 {
			for i := range end {
				if !yield(i) {
					return
				}
			}
		} else {
			for i := 0; ; i++ {
				if !yield(i) {
					return
				}
			}
		}
	}
}
