// Package wpool implements a bounded worker pool for safe, concurrent processing.
//
// The wpool module provides a concurrent processing system that wraps a channel
// and spawns a fixed number of workers to process items. It features:
//
//   - Generic type support for channel payloads
//   - Configurable number of workers for bounded concurrency
//   - Safe submission of items via Submit(context.Context, Item)
//   - Graceful shutdown with Stop(context.Context) that waits for in-flight processing
//   - Protection against resource starvation during high-concurrency bursts
//
// Use wpool when you need multiple senders with graceful shutdown capabilities
// and want to cap worker concurrency over channel receiving, especially for
// tasks that are not small/short-lived or when facing sudden bursts of submissions.
//
// Example usage:
//
//	p := wpool.NewWorkerPool[string](
//		callback,
//	)
//
//	p.Start(ctx, 10) // start 10 workers
//	p.Submit(ctx, "item")
//	p.Stop(ctx)
package wpool
