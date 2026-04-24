package core

import (
	"runtime"
	"sync"
)

// GoroutinePool is a pool of worker goroutines that execute submitted functions.
type GoroutinePool interface {
	// Run executes fn on a pool worker and blocks until fn completes.
	Run(fn func())
	// Close shuts down the pool, waiting for all workers to exit.
	// No further Run calls may be made after Close.
	Close()
}

// NewGOMAXPROCSPool creates a GoroutinePool with GOMAXPROCS worker goroutines.
// If singleThreaded is true, returns a pool that runs work immediately on the caller's goroutine.
func NewGOMAXPROCSPool(singleThreaded bool) GoroutinePool {
	if singleThreaded {
		return immediatePool{}
	}
	return newGoroutinePool(runtime.GOMAXPROCS(0))
}

type goroutinePool struct {
	work chan func()
	wg   sync.WaitGroup
}

func newGoroutinePool(size int) *goroutinePool {
	p := &goroutinePool{
		work: make(chan func()),
	}
	p.wg.Add(size)
	for range size {
		go func() {
			defer p.wg.Done()
			for fn := range p.work {
				fn()
			}
		}()
	}
	return p
}

func (p *goroutinePool) Run(fn func()) {
	var pv any
	done := make(chan struct{})
	p.work <- func() {
		defer close(done)
		defer func() {
			pv = recover()
		}()
		fn()
	}
	<-done
	if pv != nil {
		panic(pv)
	}
}

func (p *goroutinePool) Close() {
	close(p.work)
	p.wg.Wait()
}

type immediatePool struct{}

var _ GoroutinePool = immediatePool{}

func (p immediatePool) Run(fn func()) { fn() }
func (p immediatePool) Close()        {}
