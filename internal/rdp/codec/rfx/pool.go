package rfx

import (
	"runtime"
	"sync"
	"sync/atomic"
)

const tilesPerWorker = 2

type workerPool struct {
	scratch []*scratch
}

func (p *workerPool) get(i int) *scratch {
	for len(p.scratch) <= i {
		p.scratch = append(p.scratch, new(scratch))
	}
	return p.scratch[i]
}

func (p *workerPool) run(count int, sequential bool, work func(s *scratch, i int)) {
	workers := min(runtime.GOMAXPROCS(0), count/tilesPerWorker)
	if sequential || workers <= 1 {
		s := p.get(0)
		for i := range count {
			work(s, i)
		}
		return
	}
	var (
		next  atomic.Int64
		group sync.WaitGroup
	)
	loop := func(s *scratch) {
		for {
			i := int(next.Add(1) - 1)
			if i >= count {
				return
			}
			work(s, i)
		}
	}
	for w := 1; w < workers; w++ {
		s := p.get(w)
		group.Go(func() { loop(s) })
	}
	loop(p.get(0))
	group.Wait()
}
