package wpool

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

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

func BenchmarkSubmit_ShutdownModeImmediate(b *testing.B) {
	ctx := context.Background()

	subject := NewWorkerPool(noop, WithChannelBufferSize(b.N+1), WithShutdownMode(ShutdownModeImmediate))

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

func BenchmarkStop_ShutdownModeImmediate(b *testing.B) {
	ctx := context.Background()
	for range b.N {
		NewWorkerPool(noop, WithShutdownMode(ShutdownModeImmediate)).Stop(ctx)
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
	subject.workerDrain(ctx, 0)
	b.StopTimer()
}

type FullFlowBenchmarkCase struct {
	workers           int
	senders           int
	channelBufferSize int
	shutdownMode      ShutdownMode
}

func (bc FullFlowBenchmarkCase) Name() string {
	return fmt.Sprintf(
		"w%d_s%d_b%d_%s",
		bc.workers,
		bc.senders,
		bc.channelBufferSize,
		bc.shutdownMode.String(),
	)
}

func (bc FullFlowBenchmarkCase) Run(b *testing.B) {
	ctx := context.Background()

	subject := NewWorkerPool(noop, WithChannelBufferSize(bc.channelBufferSize), WithShutdownMode(bc.shutdownMode))

	start := make(chan struct{})

	wg := sync.WaitGroup{}
	wg.Add(bc.senders)

	for range bc.senders {
		go benchSender(b, ctx, &wg, start, subject)
	}
	close(start)

	subject.Start(ctx, bc.workers)

	wg.Wait()
	subject.Stop(ctx)
}

func BenchmarkFullFlow(b *testing.B) {
	tests := []FullFlowBenchmarkCase{
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

	for idx, bc := range tests {
		bc.shutdownMode = ShutdownModeDrain
		b.Run(fmt.Sprintf("%d_%s", idx, bc.Name()), bc.Run)

		bc.shutdownMode = ShutdownModeImmediate
		b.Run(fmt.Sprintf("%d_%s", idx, bc.Name()), bc.Run)
	}
}

func benchSender(b *testing.B, ctx context.Context, wg *sync.WaitGroup, start chan struct{}, subject *WorkerPool[int]) {
	b.Helper()
	defer wg.Done()
	<-start
	for i := range b.N {
		_ = subject.Submit(ctx, i)
	}
}
