package singleflight

import (
	"sync"
)

// Result is the return type of DoChan
type Result struct {
	Val interface{}
	Err error
}

// call represents an ongoing or completed request
type call struct {
	wg  sync.WaitGroup
	val interface{}
	err error
}

// Group manages all kinds of calls
type Group struct {
	mu sync.Mutex       // protects m
	m  map[string]*call // lazily initialized
}

// Do executes fn for a given key. Subsequent calls for the same key await the result.
func (g *Group) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &call{}
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	c.val, c.err = fn()
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.val, c.err
}

// DoChan executes fn for a given key and returns a channel that will receive the result.
func (g *Group) DoChan(key string, fn func() (interface{}, error)) <-chan Result {
	ch := make(chan Result, 1)
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		go func() {
			c.wg.Wait()
			ch <- Result{c.val, c.err}
		}()
		return ch
	}
	c := &call{}
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	go func() {
		c.val, c.err = fn()
		c.wg.Done()

		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()

		ch <- Result{c.val, c.err}
	}()

	return ch
}
