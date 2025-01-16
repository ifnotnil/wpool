package wpool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

type WorkerPool[T any] struct {
	logger      *slog.Logger
	ch          chan T
	stopped     chan struct{}
	cb          func(ctx context.Context, item T)
	workersWG   sync.WaitGroup
	rwMutex     sync.RWMutex
	once        sync.Once
	stoppedBool atomic.Bool
}

func NewWorkerPool[T any](logger *slog.Logger, channelBufferSize int, callback func(ctx context.Context, item T)) *WorkerPool[T] {
	return &WorkerPool[T]{
		logger:  logger,
		ch:      make(chan T, channelBufferSize),
		stopped: make(chan struct{}),
		cb:      callback,
	}
}

func (p *WorkerPool[T]) Submit(ctx context.Context, item T) error {
	if p.stoppedBool.Load() {
		return ErrWorkerPoolStopped
	}

	p.rwMutex.RLock()
	defer p.rwMutex.RUnlock()

	select {
	case <-p.stopped: // to cover the case where while waiting to send (because the channel is filled), pool stops.
		return ErrWorkerPoolStopped
	case p.ch <- item:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("worker pool item submission failed due to context cancellation: %w", ctx.Err())
	}
}

func (p *WorkerPool[T]) Start(ctx context.Context, numOfWorkers int) {
	p.logger.InfoContext(ctx, "PooledWorkers starting", slog.Int("workers_count", numOfWorkers))
	p.workersWG.Add(numOfWorkers)
	for i := range numOfWorkers {
		go p.worker(ctx, i)
	}
}

func (p *WorkerPool[T]) worker(ctx context.Context, id int) {
	defer p.workersWG.Done()

	for {
		select {
		case item, open := <-p.ch:
			if !open { // Channel has been closed.
				p.logger.DebugContext(ctx, "worker channel was closed", slog.Int("worker_id", id))
				return
			}
			p.cb(ctx, item)

		case <-ctx.Done(): // Context is done (canceled or deadline exceeded)
			p.logger.DebugContext(ctx, "worker context is done", slog.Int("worker_id", id))
			go p.Stop(ctx)
			return
		}
	}
}

var ErrWorkerPoolStopped = errors.New("worker pool is stopped")

func (p *WorkerPool[T]) Stop(ctx context.Context) {
	p.once.Do(func() { p.close(ctx) })
}

func (p *WorkerPool[T]) close(ctx context.Context) {
	p.logger.InfoContext(ctx, "PooledWorkers shutting down")
	p.stoppedBool.Store(true) // stop receiving.
	close(p.stopped)          // stop receiving.
	func() {
		p.rwMutex.Lock() // acquire read lock to close ch.
		defer p.rwMutex.Unlock()
		close(p.ch) // stop accepting.
	}()
	p.workersWG.Wait() // wait for workers to stop.
	p.logger.InfoContext(ctx, "PooledWorkers shutdown completed")
}
