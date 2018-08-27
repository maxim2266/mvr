/*
Package mvr is a minimal viable runtime that provides a top-level context which gets cancelled
when either os.Interrupt or os.Kill signal is delivered, graceful shutdown by wating for all
registered goroutines to terminate before exitting the application, simple goroutine pool, and
asynchronous logger.
*/
package mvr

import (
	"context"
	"log"
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
)

// Go starts the given function in a new goroutine registered with the runtime.
func Go(fn func()) {
	atomic.AddInt32(&gcount, 1)

	go func() {
		defer func() {
			if atomic.AddInt32(&gcount, -1) == 0 {
				signal.Stop(sigch)
				close(sigch)
			}
		}()

		fn()
	}()
}

// Run takes the application entry point function as a parameter, and executes it in a separate goroutine.
// The return value of the main function will be passed over to os.Exit(). The Run() function itself
// never returns.
func Run(main func() int) {
	// logger set-up
	log.SetOutput(logger{})

	// top-level context
	gctx, Cancel = context.WithCancel(context.Background())

	// signal handlers
	signal.Notify(sigch, os.Interrupt, os.Kill)

	// start main function
	var ret int

	Go(func() { ret = main() })

	// main loop
loop:
	for {
		select {
		case _, running := <-sigch:
			if running {
				Cancel()
			} else {
				break loop
			}

		case s := <-logch:
			os.Stderr.WriteString(s)
		}
	}

	// drain the log queue
	for {
		select {
		case s := <-logch:
			os.Stderr.WriteString(s)

		default:
			os.Exit(ret)
		}
	}
}

var gcount int32
var sigch = make(chan os.Signal, 1)
var gctx context.Context

// Cancel cancels the top-level context. This is a variable, so it can be replaced
// to perform more actions when the cancellation is being requested, provided that the
// original function is called as well.
var Cancel func()

// Context returns the top-level context.
func Context() context.Context { return gctx }

// Err returns the top-level context error.
func Err() error { return gctx.Err() }

// Done returns the top-level context's cancellation channel.
func Done() <-chan struct{} { return gctx.Done() }

// logger (implements io.Writer interface)
type logger struct{}

func (logger) Write(s []byte) (int, error) {
	logch <- string(s)
	return len(s), nil
}

var logch = make(chan string, 1000)

// Async starts the given function in a separate goroutine, and returns a channel where the returned error
// will be placed when the given function completes.
func Async(fn func() error) <-chan error {
	ch := make(chan error, 1)

	Go(func() {
		defer close(ch)

		if err := fn(); err != nil {
			ch <- err
		}
	})

	return ch
}

// Parallel invokes job functions from the given queue, sending all non-nil errors to the returned channel.
// The error channel gets closed when all the jobs from the input queue are either complete or cancelled.
// The returned cancellation function should always be called at some point to free the associated resources.
func Parallel(numThreads int, jobs <-chan func() error) (<-chan error, context.CancelFunc) {
	return ParallelCtx(Context(), numThreads, jobs)
}

// ParallelCtx invokes job functions from the given queue, sending all non-nil errors to the returned channel.
// The error channel gets closed when all the jobs from the input queue are either complete or cancelled.
// The returned cancellation function should always be called at some point to free the associated resources.
// The function also terminates when the given context is cancelled.
func ParallelCtx(parent context.Context, numThreads int, jobs <-chan func() error) (<-chan error, context.CancelFunc) {
	// check the requested number of threads
	if numThreads <= 0 {
		numThreads = runtime.NumCPU()
	}

	// derived context with cancellation function
	ctx, cancel := context.WithCancel(parent)

	// error channel
	nerr := cap(jobs)

	if nerr < numThreads { // try to allocate at least one error slot per thread
		nerr = numThreads
	}

	if nerr > 100 { // but not too many
		nerr = 100
	}

	errch := make(chan error, nerr)

	// start workers
	wc := int32(numThreads)

	for i := 0; i < numThreads; i++ {
		Go(func() {
			defer func() {
				// last worker closes the error channel
				if atomic.AddInt32(&wc, -1) == 0 {
					// report parent context error, if any
					if err := parent.Err(); err != nil {
						select {
						case errch <- err:
							// ok
						default:
							// we are closing now so we can't block
						}
					}

					close(errch)
				}
			}()

			// job executing loop
			for {
				select {
				case job, running := <-jobs:
					if !running {
						return
					}

					if err := job(); err != nil {
						select {
						case errch <- err:
							// ok
						case <-ctx.Done():
							return
						}
					}

				case <-ctx.Done():
					return
				}
			}
		})
	}

	return errch, cancel
}

// ForEachString creates a channel of jobs, where each job is an application of the given function to a string
// from the given list. The result is suitable for Parallel() function.
func ForEachString(args []string, fn func(string) error) (jobs chan func() error) {
	jobs = make(chan func() error, len(args))

	defer close(jobs)

	for _, arg := range args {
		arg := arg
		jobs <- func() error { return fn(arg) }
	}

	return
}