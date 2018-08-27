# Minimal Viable Runtime (MVR)

[![GoDoc](https://godoc.org/github.com/maxim2266/mvr?status.svg)](https://godoc.org/github.com/maxim2266/mvr)
[![Go report](http://goreportcard.com/badge/maxim2266/mvr)](http://goreportcard.com/report/maxim2266/mvr)
[![License: BSD 3-Clause](https://img.shields.io/badge/License-BSD_3--Clause-yellow.svg)](https://opensource.org/licenses/BSD-3-Clause)

## Motivation
When starting a new project there always is a certain amount of low-level code that has to be
written in order to provide for some basic runtime functionality, like top-level context, signal handlers, etc. Often this kind of code is either written from scratch, or brought in with an external library.
Programming the same functionality from scratch is usually tedious and error-prone, while external libraries may sometimes be just too heavy for the intended use, introduce significant overhead, or
impose an uncomfortable programming model. This project is an attempt to bring a number of frequently used
runtime functions into one place without introducing another fat API or adding many external dependencies.

The package adds the following functionality:
- Top-level context with signal handlers to cancel the context when a signal is delivered;
- Graceful shutdown to make sure all goroutines have completed before the application terminates;
- A simple way of running a bunch of tasks on a pool of goroutines;
- Asynchronous logging, where the actual writing to the log file is done in the background to make sure
performance-critical code is not exposed to the i/o latencies of writing to the log.


## Application entry point
The application entry point should be a function of type `func() int`, returning an integer error code
that will be passed down to `os.Exit()` when the application terminates. Typically, the entry point is
invoked like:
```go
func main() {
	mvr.Run(appMain)
}

func appMain() int { ... }
```
The `mvr.Run()` function never returns.

## Top-level context
The top-level context gets initialised (along with the rest of the package) when the application
invokes `mvr.Run()` function. The context is accessible via `mvr.Context()` function, with the
shortcuts `mvr.Done()` and `mvr.Err()` giving access to the corresponding methods of the top context.
The context is cancelled when either `os.Interrupt` or `os.Kill` signal is delivered,
or `mvr.Cancel()` function is invoked. Internally, the signal handlers simply call the `mvr.Cancel()`
function, which by default only cancels the top context. The `mvr.Cancel()` function is
exposed as a variable, so that it can be replaced with a user-defined function. The replacement function
must call the original cancellation function at some point, apart from that it is free to perform
any other action.

## Goroutine invocation
In order to ensure graceful shutdown the package keeps track of all goroutines invoked
(directly or indirectly) through its API. The simplest way to start a goroutine is `mvr.Go()` function that
provides functionality similar to the `go` keyword. Another way of running a function in a separate
goroutine is `mvr.Async()`, which takes a function to launch, of type `func() error`, and returns
a channel to which the error (if any) will be delivered upon the function completion.
Typical usage scenario:
```go
// start a function
errch := mvr.Async(func() error {
	// ...
	return err
})

// do other things here...

// wait for completion and check the error
if err := <-errch; err != nil {
	// handle the error
}

// another option: simply wait for completion and return (aka Await)
return <-errch
```

## Goroutine pool
As simple example of executing tasks on a pool of goroutines consider the case where a number of files
need to be compressed in parallel:
```go
// define a function that compresses one file
func compressFile(name string) error { ... }

// a list of files to compress (fixed list for this example)
files := []string{"aaa.json", "bbb.json", "ccc.json", "ddd.json"}

// start parallel compression using 2 goroutines
errch, cancel := mvr.Parallel(2, mvr.ForEachString(files, compressFile))

defer cancel()	// to clean the associated resources afterwards

// do other things...

// retrieve errors (the error channel is closed when the processing is done)
for err := range errch {
	// process the error
}

// another option: wait to get the first error (if any) and stop further processing.
// if there is no error, then the pool runs to completion and the channel gets closed, returning nil
return <-errch
```

The second parameter to `mvr.Parallel()` is a channel of tasks, so in a more advanced scenario
there may be a separate goroutine continuously supplying tasks to the pool, like in the
following example adapted from `mvr_test.go`:
```go
func TestParallelFeed(t *testing.T) {
	const N = 10	// number of tasks

	var res int32

	// input task channel (in practice should probably have some non-zero size)
	inch := make(chan func() error)

	// start feeder
	mvr.Go(func() {
		defer close(inch) // don't forget this!

		for i := 0; i < N; i++ {
			inch <- func() error {
				atomic.AddInt32(&res, 1) // just for this example
				return nil
			}
		}
	})

	// launch tasks
	errs, cancel := mvr.Parallel(0, inch) // pool of runtime.NumCPU() goroutines

	defer cancel()

	// check errors
	for err := range errs {
		t.Error(err)
		return
	}

	// etc.
}
```

There is another function, `mvr.ParallelCtx()`, that takes a `context.Context` as its first parameter
to allow for a user-managed context to control the goroutine pool.

## Logging
The package does _not_ replace the logger from the standard library, and it provides no additional
API. Instead, the library replaces the target `io.Writer` to which the logger writes. This _should_
have no effect on any other logging layer built on top of the standard `log` package. The log
writes to `os.Stderr`.

## Testing
For unit-testing of an application utilising this package the correct initialisation of the runtime can be
ensured by defining `TextMain` function from which all the tests are invoked, typically:
```go
func TestMain(m *testing.M) {
	mvr.Run(m.Run)
}
```

## Limitations
- The package has **no way** of intercepting calls to terminating functions like
`log.Fatal()` or `os.Exit()`, and no guarantees can be given if any of those functions is invoked.
- The package replaces the `io.Writer` used by the standard logger, so the writer should not be replaced
again by the application;
- Only goroutines started via the package API are waited on before termination.

#### Project status
Tested on Linux Mint 18.3 using Go version 1.10.3.

