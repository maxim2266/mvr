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

package mvr

import (
	"context"
	"errors"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	rand.Seed(time.Now().UnixNano())
	Run(m.Run)
}

func TestGo(t *testing.T) {
	const N = 100

	var res int32
	var wg sync.WaitGroup

	wg.Add(N)

	for i := 0; i < N; i++ {
		Go(func() {
			defer wg.Done()

			time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond)
			atomic.AddInt32(&res, 1)
		})
	}

	wg.Wait()

	if atomic.LoadInt32(&res) != N {
		t.Error("Invalid number of iterations:", res, "instead of", N)
		return
	}
}

func TestAsync(t *testing.T) {
	const msg = "ERROR"

	var res int32

	err := <-Async(func() error {
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&res, 1)

		return errors.New(msg)
	})

	if err == nil {
		t.Error("Missing error message")
		return
	}

	if err.Error() != msg {
		t.Error("Unexpected error message:", err.Error())
		return
	}

	if atomic.LoadInt32(&res) != 1 {
		t.Error("Unexpected result:", res, "instead of 1")
		return
	}
}

func TestParallel(t *testing.T) {
	nums := []int{1, runtime.NumCPU(), 50, rand.Intn(10) + 1}

	for _, nprocs := range nums {
		if !testParallel(nprocs, rand.Intn(nprocs*10), t) {
			return
		}
	}
}

func testParallel(nprocs, njobs int, t *testing.T) bool {
	var counter int32

	errs, cancel := Parallel(nprocs, counterJobs(&counter, njobs))

	defer cancel()

	for err := range errs {
		t.Error("nprocs:", nprocs, ", failure:", err)
		return false
	}

	if counter != int32(njobs) {
		t.Error("nprocs:", nprocs, ", failure: Invalid number of completed jobs:", counter, "instead of", njobs)
		return false
	}

	return true
}

func TestParallelForEach(t *testing.T) {
	input := []string{"1", "2", "3", "4"}

	var res int32

	errs, cancel := Parallel(0, ForEachString(input, func(s string) error {
		val, err := strconv.Atoi(s)

		if err != nil {
			return err
		}

		atomic.AddInt32(&res, int32(val))
		return nil
	}))

	defer cancel()

	for err := range errs {
		t.Error(err)
		return
	}

	if res != 10 {
		t.Error("Invalid number of completed jobs:", res, "instead of 10")
		return
	}
}

func TestParallelForEachError(t *testing.T) {
	input := []string{"1", "2", "zzz", "4"}

	var res int32

	errs, cancel := Parallel(0, ForEachString(input, func(s string) error {
		val, err := strconv.Atoi(s)

		if err != nil {
			return err
		}

		atomic.AddInt32(&res, int32(val))
		return nil
	}))

	defer cancel()

	err := <-errs

	if err == nil {
		t.Error("Missing error")
		return
	}

	if _, ok := err.(*strconv.NumError); !ok {
		t.Error("Unexpected error:", err)
		return
	}

	if err := <-errs; err != nil {
		t.Error("Unexpected second error:", err)
		return
	}

	if res > 7 {
		t.Error("Invalid number of completed jobs:", res)
		return
	}
}

func TestParallelCancel(t *testing.T) {
	ctx, stop := context.WithCancel(Context())

	var res int32

	errs, cancel := ParallelCtx(ctx, 0, counterJobs(&res, 5))

	defer cancel()

	stop()

	err := <-errs

	if err == nil {
		t.Error("Missing error")
		return
	}

	if err != context.Canceled {
		t.Error("Unexpected error:", err)
		return
	}

	if err = <-errs; err != nil {
		t.Error("Unexpected second error:", err)
		return
	}
}

func counterJobs(pcnt *int32, njobs int) (jobs chan func() error) {
	jobs = make(chan func() error, njobs)

	defer close(jobs)

	for i := 0; i < njobs; i++ {
		jobs <- func() error {
			time.Sleep(time.Duration(rand.Intn(20)) * time.Millisecond)
			atomic.AddInt32(pcnt, 1)
			return nil
		}
	}

	return
}

func TestParallelFeed(t *testing.T) {
	const N = 10

	var res int32

	// input tasks channel
	inch := make(chan func() error)

	// start feeder
	Go(func() {
		defer close(inch)

		time.Sleep(10 * time.Millisecond)

		for i := 0; i < N; i++ {
			inch <- func() error {
				atomic.AddInt32(&res, 1)
				return nil
			}
		}
	})

	// launch tasks
	errs, cancel := Parallel(0, inch)

	defer cancel()

	// check errors
	for err := range errs {
		t.Error(err)
		return
	}

	if res != 10 {
		t.Error("Unexpected result:", res, "instead of", N)
		return
	}
}

func TestLogger(t *testing.T) {
	// logger flags
	flags := log.Flags()

	log.SetFlags(0)

	defer log.SetFlags(flags)

	// create temp. file
	tmp, err := ioutil.TempFile(os.TempDir(), "tmp")

	if err != nil {
		t.Error(err)
		return
	}

	fname := tmp.Name()

	defer func() {
		tmp.Close()
		os.Remove(fname)
	}()

	t.Log("Temp. file:", fname)

	// replace stderr
	prev := os.Stderr
	os.Stderr = tmp

	defer func() { os.Stderr = prev }()

	// write some log
	log.Println("zzz")
	log.Println("aaa")
	log.Println("bbb")

	time.Sleep(10 * time.Millisecond)

	if err = tmp.Sync(); err != nil {
		t.Error(err)
		return
	}

	// read result
	s, err := ioutil.ReadFile(fname)

	if err != nil {
		t.Error(err)
		return
	}

	// check
	if res := string(s); res != "zzz\naaa\nbbb\n" {
		t.Errorf("Unexpected string: %q", res)
		return
	}
}

func _TestSignal(t *testing.T) {
	// press Ctrl+c to continue
	<-Done()
	t.Log(Err())
}
