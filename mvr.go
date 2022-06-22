// +build !windows

/*
Copyright (c) 2018,2019 Maxim Konakov
All rights reserved.

Redistribution and use in source and binary forms, with or without modification,
are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
   this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.
3. Neither the name of the copyright holder nor the names of its contributors
   may be used to endorse or promote products derived from this software without
   specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED.
IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT,
INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING,
BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY
OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE,
EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
*/

/*
Package mvr is a minimal viable runtime that provides a top-level context cancelled by Unix signals,
graceful shutdown by waiting for all registered goroutines to terminate before exitting the application,
simple goroutine pool, and asynchronous logger.
*/
package mvr

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
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
// The return value of the main function will be passed over to os.Exit(). Exit from the main function also
// cancels the top-level context. The Run() function itself never returns.
func Run(main func() int) {
	// logger set-up
	logit := getLogWriter()

	log.SetOutput(logger{})

	// signal handlers
	signal.Notify(sigch, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)

	// start main function
	var ret int32

	Go(func() {
		defer Cancel()
		atomic.CompareAndSwapInt32(&ret, 0, int32(main()))
	})

	// main loop
	var err error

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
			if err == nil {
				if _, err = logit(s); err != nil {
					// the logger has failed, initiate shutdown
					atomic.CompareAndSwapInt32(&ret, 0, -42)
					Cancel()
				}
			}
		}
	}

	// drain the log queue
	for {
		select {
		case s := <-logch:
			if err == nil {
				_, err = logit(s)
			}
		default:
			os.Exit(int(ret))
		}
	}
}

func getLogWriter() func(string) (int, error) {
	w := log.Writer()

	if sw, ok := w.(io.StringWriter); ok {
		return sw.WriteString
	}

	return func(s string) (int, error) {
		return w.Write([]byte(s))
	}
}

var gcount int32
var sigch = make(chan os.Signal, 1)
var gctx, gcancel = context.WithCancel(context.Background())

// Cancel cancels the top-level context.
func Cancel() { gcancel() }

// Context returns the top-level context.
func Context() context.Context { return gctx }

// Err returns the top-level context error.
func Err() error { return gctx.Err() }

// Done returns the top-level context's cancellation channel.
func Done() <-chan struct{} { return gctx.Done() }

// OnCancel registers a function to be called when the top-level context gets cancelled. The timeout parameter
// specifies the time for the function to complete its task; if set to 0 the timeout will be DefaultCancellationTimeout.
// The supplied function will be called with a context that is cancelled when the timeout expires.
func OnCancel(timeout time.Duration, fn func(context.Context)) {
	if timeout <= 0 {
		timeout = DefaultCancellationTimeout
	}

	Go(func() {
		<-Done()

		// context.Background() because the main context has just been cancelled
		ctx, cancel := context.WithTimeout(context.Background(), timeout)

		go func() {
			defer cancel()
			fn(ctx)
		}()

		<-ctx.Done()
	})
}

// DefaultCancellationTimeout is the default time given to a cancellation function to complete.
const DefaultCancellationTimeout = 10 * time.Second

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
