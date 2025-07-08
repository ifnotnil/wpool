// Package wpool implements a bounded worker pool for concurrent processing.
//
// The wpool provides a safe, concurrent processing system by wrapping a channel
// and spawning a fixed number of workers to process items.
package wpool
