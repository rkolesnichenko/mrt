package mrt

import "testing"

// TestReapTakesFinishedZombies: reap takes the result of a dispatched job the
// chain has passed once a worker has sent it, subtracting its bytes from the
// results counter and its job from what is outstanding, and keeps the jobs
// whose results are still to come, in order, clearing the slots it frees.
func TestReapTakesFinishedZombies(t *testing.T) {
	var held int64
	z := &parallelBzip2{outstanding: 3, segHeld: 600}
	z.results = &resultBytes{hook: func(n int64) { held = n }}
	z.results.add(1000 + 7) // the finished job's 1,000 bytes and 7 of another's
	job := func(seg int) *bzip2Job {
		return &bzip2Job{seg: make([]byte, seg), res: make(chan bzip2Result, 1)}
	}
	pending1, done, pending2 := job(100), job(200), job(300)
	done.res <- bzip2Result{data: make([]byte, 1000)}
	z.zombies = []*bzip2Job{pending1, done, pending2}
	z.reap()
	if held != 7 {
		t.Errorf("results counter %d after reap, want 7", held)
	}
	if z.outstanding != 2 || z.segHeld != 400 {
		t.Errorf("outstanding %d, segment bytes %d; want 2 and 400", z.outstanding, z.segHeld)
	}
	if len(z.zombies) != 2 || z.zombies[0] != pending1 || z.zombies[1] != pending2 {
		t.Errorf("zombies %v, want the two pending jobs in order", z.zombies)
	}
	if z.zombies[:3][2] != nil {
		t.Error("the freed slot still holds a job")
	}
}
