package wpool

import (
	"context"
	"sync"
	"testing"
	"time"

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

	t.Run("close while ctx cancel", func(t *testing.T) {
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
}
