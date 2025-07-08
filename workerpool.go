package wpool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

type ShutdownMode int

const (
	ShutdownModeDrain ShutdownMode = iota
	ShutdownModeImmediate
)

func (s ShutdownMode) String() string {
	switch s {
	case ShutdownModeDrain:
		return "Drain"
	case ShutdownModeImmediate:
		return "Immediate"
	default:
		return "unknown"
	}
}

type config struct {
	logger            *slog.Logger
	channelBufferSize int
	shutdownMode      ShutdownMode
}

func defaultConfig() config {
	return config{
		logger:            slog.New(disabledSlogHandler{}),
		channelBufferSize: 0,
	}
}

func WithChannelBufferSize(s int) func(*config) {
	return func(c *config) { c.channelBufferSize = s }
}

func WithLogger(l *slog.Logger) func(*config) {
	return func(c *config) { c.logger = l }
}

func WithShutdownMode(m ShutdownMode) func(*config) {
	return func(c *config) { c.shutdownMode = m }
}

type WorkerPool[T any] struct {
	logger        *slog.Logger
	ch            chan T
	stopReceiving chan struct{}
	stopWorkers   chan struct{}
	cb            func(ctx context.Context, item T)
	workersWG     sync.WaitGroup
	chRWMutex     sync.RWMutex
	shutdownOnce  sync.Once
	shutdownMode  ShutdownMode
}

func NewWorkerPool[T any](callback func(ctx context.Context, item T), opts ...func(*config)) *WorkerPool[T] {
	c := defaultConfig()

	for _, o := range opts {
		o(&c)
	}

	return &WorkerPool[T]{
		logger:        c.logger,
		ch:            make(chan T, c.channelBufferSize),
		stopReceiving: make(chan struct{}),
		stopWorkers:   make(chan struct{}),
		cb:            callback,
		workersWG:     sync.WaitGroup{},
		chRWMutex:     sync.RWMutex{},
		shutdownOnce:  sync.Once{},
		shutdownMode:  c.shutdownMode,
	}
}

func (p *WorkerPool[T]) Submit(ctx context.Context, item T) error {
	switch p.shutdownMode {
	case ShutdownModeImmediate:
		return p.submitNoLock(ctx, item)
	default: // ShutdownModeDrain
		return p.submitWithLock(ctx, item)
	}
}

func (p *WorkerPool[T]) submitWithLock(ctx context.Context, item T) error {
	p.chRWMutex.RLock() // acquire read lock to send to ch.
	defer p.chRWMutex.RUnlock()

	// To avoid writing to a closed p.ch and cause a panic, check if we are stopped first. In the select below, this can
	// NOT happen, because p.ch cannot close while we hold the lock.
	select {
	case <-p.stopReceiving:
		return ErrWorkerPoolStopped
	case <-ctx.Done():
		return fmt.Errorf("worker pool item submission failed due to context cancellation: %w", ctx.Err())
	default:
	}

	// this code is duplicated here and in submitNoLock, because it seems it does not get inlined, due to select statement.
	// Lets verify that and refactor if needed.
	select {
	case <-p.stopReceiving: // to cover the case where while waiting to send (because the channel is filled), pool stops.
		return ErrWorkerPoolStopped
	case p.ch <- item:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("worker pool item submission failed due to context cancellation: %w", ctx.Err())
	}
}

func (p *WorkerPool[T]) submitNoLock(ctx context.Context, item T) error {
	select {
	case <-p.stopReceiving: // to cover the case where while waiting to send (because the channel is filled), pool stops.
		return ErrWorkerPoolStopped
	case p.ch <- item:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("worker pool item submission failed due to context cancellation: %w", ctx.Err())
	}
}

func (p *WorkerPool[T]) Start(ctx context.Context, numOfWorkers int) {
	p.logger.InfoContext(ctx, "worker pool starting", slog.Int("workers_count", numOfWorkers))
	if numOfWorkers <= 0 {
		return
	}
	p.workersWG.Add(numOfWorkers)
	for i := range numOfWorkers {
		switch p.shutdownMode {
		case ShutdownModeImmediate:
			go p.workerImmediate(ctx, i)
		default: // ShutdownModeDrain
			go p.workerDrain(ctx, i)
		}
	}
}

func (p *WorkerPool[T]) workerDrain(ctx context.Context, id int) {
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

func (p *WorkerPool[T]) workerImmediate(ctx context.Context, id int) {
	defer p.workersWG.Done()

	for {
		select {
		case item, open := <-p.ch:
			if !open { // Channel has been closed.
				p.logger.DebugContext(ctx, "worker channel was closed", slog.Int("worker_id", id))
				return
			}
			p.cb(ctx, item)

		case <-p.stopWorkers:
			p.logger.DebugContext(ctx, "worker is done", slog.Int("worker_id", id))
			return

		case <-ctx.Done(): // Context is done (canceled or deadline exceeded)
			p.logger.DebugContext(ctx, "worker context is done", slog.Int("worker_id", id))
			go p.Stop(ctx)
			return
		}
	}
}

var ErrWorkerPoolStopped = errors.New("worker pool is stopped")

func (p *WorkerPool[T]) Stop(ctx context.Context) {
	p.shutdownOnce.Do(func() {
		switch p.shutdownMode {
		case ShutdownModeImmediate:
			p.shutdownImmediate(ctx)
		default: // ShutdownModeDrain
			p.shutdownDrain(ctx)
		}
	})
}

func (p *WorkerPool[T]) shutdownDrain(ctx context.Context) {
	p.logger.InfoContext(ctx, "worker pool shutting down")

	close(p.stopReceiving) // stop receiving.

	stopRoutine := make(chan struct{})
	go func() {
		// if ctx gets canceled we need to stop the blocking shutdown flow,
		// and stop all workers.
		select {
		case <-ctx.Done():
			close(p.stopWorkers) // stop workers.
			return
		case <-stopRoutine:
			return
		}
	}()

	func() {
		p.chRWMutex.Lock() // acquire write lock to close ch.
		defer p.chRWMutex.Unlock()
		close(p.ch) // stop accepting items.
	}()

	p.workersWG.Wait() // wait for workers to stop.

	close(stopRoutine)
	p.logger.InfoContext(ctx, "worker pool shutdown completed")
}

func (p *WorkerPool[T]) shutdownImmediate(ctx context.Context) {
	p.logger.InfoContext(ctx, "worker pool shutting down")
	close(p.stopReceiving) // stop receiving.
	close(p.stopWorkers)   // stop workers.
	p.workersWG.Wait()     // wait for workers to stop.
	p.logger.InfoContext(ctx, "worker pool shutdown completed")
}
