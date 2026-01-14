package singleflight

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDo(t *testing.T) {
	var g Group
	v, err := g.Do("key", func() (interface{}, error) {
		return "bar", nil
	})
	if v != "bar" || err != nil {
		t.Errorf("Do v = %v, error = %v", v, err)
	}
}

func TestDoErr(t *testing.T) {
	var g Group
	someErr := errors.New("some error")
	v, err := g.Do("key", func() (interface{}, error) {
		return nil, someErr
	})
	if err != someErr {
		t.Errorf("Do error = %v", err)
	}
	if v != nil {
		t.Errorf("Do v = %v", v)
	}
}

func TestDoDupSuppress(t *testing.T) {
	var g Group
	var calls int32
	fn := func() (interface{}, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(10 * time.Millisecond)
		return "bar", nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			v, err := g.Do("key", fn)
			if v != "bar" || err != nil {
				t.Errorf("Do v = %v, error = %v", v, err)
			}
			wg.Done()
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Errorf("number of calls = %v; want 1", calls)
	}
}

func TestDoChan(t *testing.T) {
	var g Group
	ch := g.DoChan("key", func() (interface{}, error) {
		time.Sleep(100 * time.Millisecond)
		return "bar", nil
	})

	select {
	case res := <-ch:
		if res.Val != "bar" || res.Err != nil {
			t.Errorf("DoChan res = %v; want bar, nil", res)
		}
	case <-time.After(200 * time.Millisecond):
		t.Errorf("DoChan timed out")
	}
}

func TestDoChanDupSuppress(t *testing.T) {
	var g Group
	var calls int32
	fn := func() (interface{}, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond)
		return "bar", nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			ch := g.DoChan("key", fn)
			res := <-ch
			if res.Val != "bar" || res.Err != nil {
				t.Errorf("DoChan v = %v, error = %v", res.Val, res.Err)
			}
			wg.Done()
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Errorf("number of calls = %v; want 1", calls)
	}
}
