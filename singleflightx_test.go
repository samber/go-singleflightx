package singleflightx

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDoX(t *testing.T) {
	var g Group[string, string]

	// single
	v := g.DoX([]string{"key"}, func(keys []string) (map[string]string, error) {
		return map[string]string{"key": "bar"}, nil
	})
	assert.Len(t, v, 1)
	assert.Equal(t, "bar", v["key"].Value.Value)
	assert.Nil(t, v["key"].Err)
	assert.True(t, v["key"].Value.Valid)
	assert.Len(t, g.m, 0)

	// none
	v = g.DoX([]string{}, func(keys []string) (map[string]string, error) {
		return map[string]string{"key": "bar"}, nil
	})
	assert.Len(t, v, 0)
	assert.Len(t, g.m, 0)

	// many
	v = g.DoX([]string{"a", "b"}, func(keys []string) (map[string]string, error) {
		return map[string]string{"a": "foo", "b": "bar"}, nil
	})
	assert.Len(t, v, 2)
	assert.Equal(t, "foo", v["a"].Value.Value)
	assert.Equal(t, "bar", v["b"].Value.Value)
	assert.Nil(t, v["a"].Err)
	assert.Nil(t, v["b"].Err)
	assert.True(t, v["a"].Value.Valid)
	assert.True(t, v["b"].Value.Valid)
	assert.Len(t, g.m, 0)

	// dup
	v = g.DoX([]string{"a", "a"}, func(keys []string) (map[string]string, error) {
		return map[string]string{"a": "foo", "b": "bar"}, nil
	})
	assert.Len(t, v, 1)
	assert.Equal(t, "foo", v["a"].Value.Value)
	assert.Nil(t, v["a"].Err)
	assert.True(t, v["a"].Value.Valid)
	assert.Len(t, g.m, 0)

	// many but partial result
	v = g.DoX([]string{"a", "b"}, func(keys []string) (map[string]string, error) {
		return map[string]string{"a": "foo", "c": "bar"}, nil
	})
	assert.Len(t, v, 2)
	assert.Equal(t, "foo", v["a"].Value.Value)
	assert.Equal(t, "", v["b"].Value.Value)
	assert.Nil(t, v["a"].Err)
	assert.Nil(t, v["b"].Err)
	assert.True(t, v["a"].Value.Valid)
	assert.False(t, v["b"].Value.Valid)
	assert.Len(t, g.m, 0)
}

func TestDoXErr(t *testing.T) {
	var g Group[string, string]
	someErr := errors.New("Some error")

	v := g.DoX([]string{"a", "b"}, func(keys []string) (map[string]string, error) {
		return nil, someErr
	})
	assert.Len(t, v, 2)
	assert.Equal(t, "", v["a"].Value.Value)
	assert.Equal(t, "", v["b"].Value.Value)
	assert.True(t, someErr == v["a"].Err)
	assert.True(t, someErr == v["b"].Err)
	assert.False(t, v["a"].Value.Valid)
	assert.False(t, v["b"].Value.Valid)
	assert.Len(t, g.m, 0)
}

func TestDoXDupSuppress(t *testing.T) {
	var g Group[string, string]
	var wg1, wg2 sync.WaitGroup
	c := make(chan string, 1)
	var calls int32
	fn := func(keys []string) (map[string]string, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// First invocation.
			wg1.Done()
		}
		v := <-c
		c <- v // pump; make available for any future calls

		time.Sleep(10 * time.Millisecond) // let more goroutines enter Do

		return map[string]string{"a": "foo", "b": "bar"}, nil
	}

	const n = 10
	wg1.Add(1)
	for i := 0; i < n; i++ {
		wg1.Add(1)
		wg2.Add(1)

		go func() {
			defer wg2.Done()
			wg1.Done()
			v := g.DoX([]string{"a", "b"}, fn)
			assert.Len(t, v, 2)
			assert.Equal(t, "foo", v["a"].Value.Value)
			assert.Equal(t, "bar", v["b"].Value.Value)
			assert.Nil(t, v["a"].Err)
			assert.Nil(t, v["b"].Err)
			assert.True(t, v["a"].Value.Valid)
			assert.True(t, v["b"].Value.Valid)
			g.mu.Lock()
			assert.Len(t, g.m, 0)
			g.mu.Unlock()
		}()
	}

	wg1.Wait()

	g.mu.Lock()
	assert.Len(t, g.m, 2)
	g.mu.Unlock()

	// At least one goroutine is in fn now and all of them have at
	// least reached the line before the Do.
	c <- "bar"
	wg2.Wait()
	if got := atomic.LoadInt32(&calls); got <= 0 || got >= n {
		t.Errorf("number of calls = %d; want over 0 and less than %d", got, n)
	}

	g.mu.Lock()
	assert.Len(t, g.m, 0)
	g.mu.Unlock()
}

func TestDoChanX(t *testing.T) {
	var g Group[string, string]

	chans := g.DoChanX([]string{"a", "b"}, func(keys []string) (map[string]string, error) {
		return map[string]string{"a": "foo", "b": "bar"}, nil
	})

	assert.Len(t, chans, 2)
	res1 := <-chans["a"]
	res2 := <-chans["b"]

	assert.Equal(t, "foo", res1.Value.Value)
	assert.Equal(t, "bar", res2.Value.Value)
	assert.Nil(t, res1.Err)
	assert.Nil(t, res2.Err)
	assert.True(t, res1.Value.Valid)
	assert.True(t, res2.Value.Valid)
	assert.Len(t, g.m, 0)

	chans = g.DoChanX([]string{"a", "b"}, func(keys []string) (map[string]string, error) {
		return map[string]string{"a": "foo", "b": "bar"}, assert.AnError
	})

	assert.Len(t, chans, 2)
	res1 = <-chans["a"]
	res2 = <-chans["b"]

	assert.Equal(t, "foo", res1.Value.Value)
	assert.Equal(t, "bar", res2.Value.Value)
	assert.True(t, assert.AnError == res1.Err)
	assert.True(t, assert.AnError == res2.Err)
	assert.True(t, res1.Value.Valid)
	assert.True(t, res2.Value.Valid)
	assert.Len(t, g.m, 0)
}

// Test singleflight behaves correctly after Do panic.
// See https://github.com/golang/go/issues/41133
func TestPanicDoX(t *testing.T) {
	var g Group[string, int]
	fn := func(keys []string) (map[string]int, error) {
		panic("invalid memory address or nil pointer dereference")
	}

	const n = 5
	waited := int32(n)
	panicCount := int32(0)
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			defer func() {
				if err := recover(); err != nil {
					t.Logf("Got panic: %v\n%s", err, debug.Stack())
					atomic.AddInt32(&panicCount, 1)
				}

				if atomic.AddInt32(&waited, -1) == 0 {
					close(done)
				}
			}()

			g.DoX([]string{"key"}, fn)
		}()
	}

	select {
	case <-done:
		if panicCount != n {
			t.Errorf("Expect %d panic, but got %d", n, panicCount)
		}
	case <-time.After(time.Second):
		t.Fatalf("DoX hangs")
	}
}

func TestGoexitDoX(t *testing.T) {
	var g Group[string, *string]
	fn := func(keys []string) (map[string]*string, error) {
		runtime.Goexit()
		return nil, nil
	}

	const n = 5
	waited := int32(n)
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			var err error
			defer func() {
				if err != nil {
					t.Errorf("Error should be nil, but got: %v", err)
				}
				if atomic.AddInt32(&waited, -1) == 0 {
					close(done)
				}
			}()
			_ = g.DoX([]string{"key"}, fn)
		}()
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("DoX hangs")
	}
}

func TestPanicDoChanX(t *testing.T) {
	if os.Getenv("TEST_PANIC_DOCHAN") != "" {
		defer func() {
			recover() //nolint:errcheck
		}()

		g := new(Group[string, int])
		ch := g.DoChanX([]string{"foo"}, func([]string) (map[string]int, error) {
			panic("Panicking in DoChanX")
		})
		<-ch["foo"]
		t.Fatalf("DoChanX unexpectedly returned")
	}

	t.Parallel()

	cmd := exec.Command(executable(t), "-test.run="+t.Name(), "-test.v")
	cmd.Env = append(os.Environ(), "TEST_PANIC_DOCHAN=1")
	out := new(bytes.Buffer)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	err := cmd.Wait()
	t.Logf("%s:\n%s", strings.Join(cmd.Args, " "), out)
	if err == nil {
		t.Errorf("Test subprocess passed; want a crash due to panic in DoChanX")
	}
	if bytes.Contains(out.Bytes(), []byte("DoChanX unexpectedly")) {
		t.Errorf("Test subprocess failed with an unexpected failure mode.")
	}
	if !bytes.Contains(out.Bytes(), []byte("Panicking in DoChanX")) {
		t.Errorf("Test subprocess failed, but the crash isn't caused by panicking in DoChanX")
	}
}

func TestPanicDoXSharedByDoChanX(t *testing.T) {
	if os.Getenv("TEST_PANIC_DOCHAN") != "" {
		blocked := make(chan struct{})
		unblock := make(chan struct{})

		g := new(Group[string, int])
		go func() {
			defer func() {
				recover() //nolint:errcheck
			}()
			g.DoX([]string{"foo"}, func([]string) (map[string]int, error) {
				close(blocked)
				<-unblock
				panic("Panicking in DoX")
			})
		}()

		<-blocked
		ch := g.DoChanX([]string{"foo"}, func([]string) (map[string]int, error) {
			panic("DoChanX unexpectedly executed callback")
		})
		close(unblock)
		<-ch["foo"]
		t.Fatalf("DoChanX unexpectedly returned")
	}

	t.Parallel()

	cmd := exec.Command(executable(t), "-test.run="+t.Name(), "-test.v")
	cmd.Env = append(os.Environ(), "TEST_PANIC_DOCHAN=1")
	out := new(bytes.Buffer)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	err := cmd.Wait()
	t.Logf("%s:\n%s", strings.Join(cmd.Args, " "), out)
	if err == nil {
		t.Errorf("Test subprocess passed; want a crash due to panic in DoX shared by DoChanX")
	}
	if bytes.Contains(out.Bytes(), []byte("DoChanX unexpectedly")) {
		t.Errorf("Test subprocess failed with an unexpected failure mode.")
	}
	if !bytes.Contains(out.Bytes(), []byte("Panicking in DoX")) {
		t.Errorf("Test subprocess failed, but the crash isn't caused by panicking in DoX")
	}
}
