package wpool

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestWorkerPoolLifeCycle(t *testing.T) {
	ctx := context.Background()

	t.Run("noop", func(t *testing.T) {
		cb := func(ctx context.Context, item int) {}
		subject := NewWorkerPool(cb)
		subject.Start(ctx, 10)
		subject.Stop(ctx)
	})

	t.Run("noop multiple stops", func(t *testing.T) {
		cb := func(ctx context.Context, item int) {}
		subject := NewWorkerPool(cb)
		subject.Start(ctx, 10)
		subject.Stop(ctx)
		subject.Stop(ctx)
		subject.Stop(ctx)
	})

	t.Run("buffered channel", func(t *testing.T) {
		cb := func(ctx context.Context, item int) {}
		subject := NewWorkerPool(cb, WithChannelBufferSize(10))
		err := subject.Submit(ctx, 1)
		require.NoError(t, err)
		err = subject.Submit(ctx, 2)
		require.NoError(t, err)
		err = subject.Submit(ctx, 3)
		require.NoError(t, err)
	})

	t.Run("submit fails because of ctx cancellation", func(t *testing.T) {
		ctx2, cancel := context.WithCancel(context.Background())

		cb := func(ctx context.Context, item int) {}
		subject := NewWorkerPool(cb)

		// don't start workers to block the submit.

		go func() {
			time.Sleep(200 * time.Millisecond)
			cancel()
		}()

		err := subject.Submit(ctx2, 1)
		ErrorStringContains("context canceled")(t, err)
	})

	t.Run("submit fails because pool closes", func(t *testing.T) {
		cb := func(ctx context.Context, item int) {}
		subject := NewWorkerPool(cb)

		// don't start workers to block the submit.
		subject.Start(ctx, 0) // noop start

		go func() {
			time.Sleep(200 * time.Millisecond)
			subject.close(ctx)
		}()

		err := subject.Submit(ctx, 1)
		ErrorIs(ErrWorkerPoolStopped)(t, err)
	})

	t.Run("send to closed pool", func(t *testing.T) {
		cb := func(_ context.Context, _ int) {}
		subject := NewWorkerPool(cb)
		subject.Start(ctx, 10)
		subject.Stop(ctx)
		err := subject.Submit(ctx, 1)
		ErrorIs(ErrWorkerPoolStopped)(t, err)
	})

	t.Run("close while sending", func(t *testing.T) {
		cb := func(_ context.Context, it int) {
			time.Sleep(60 * time.Millisecond)
		}
		subject := NewWorkerPool(cb)
		subject.Start(ctx, 10)

		wg := sync.WaitGroup{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				err := subject.Submit(ctx, i)
				if err != nil {
					ErrorIs(ErrWorkerPoolStopped)(t, err)
				}
			}
		}()

		subject.Stop(ctx)
		wg.Wait()
	})

	t.Run("close with ctx cancel while sending", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cb := func(_ context.Context, it int) {
			time.Sleep(60 * time.Millisecond)
		}
		subject := NewWorkerPool(cb)
		subject.Start(ctx, 10)

		wg := sync.WaitGroup{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				err := subject.Submit(context.Background(), i)
				if err != nil {
					ErrorIs(ErrWorkerPoolStopped)(t, err)
				}
			}
		}()
		cancel()

		wg.Wait()
		subject.Stop(ctx)
	})

	t.Run("noop with logs", func(t *testing.T) {
		cb := func(ctx context.Context, item int) {}
		subject := NewWorkerPool(cb, WithLogger(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))))
		subject.Start(ctx, 10)
		subject.Stop(ctx)
	})
}
