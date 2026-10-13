package github

import (
	"sync"
)

// Coordinator provides compare-and-swap and idempotency for backends that
// lack native conditional updates (GitHub Issues). It must serialize every
// authoritative mutation (claim, materialize, ...) across every Adapter
// instance that can reach the same project: a lock that some other
// instance targeting the same project does not also wait on gives no CAS
// guarantee at all, however correct the read-evaluate-write sequence it
// guards looks in isolation.
type Coordinator interface {
	WithLock(fn func())
}

// InProcessCoordinator is a process-local mutex. It only serializes calls
// made from within the same OS process, so it is safe exclusively in
// single-player mode, where exactly one Adapter instance is the active
// claimant for the project (docs/architecture.md, "Claim coordinator").
// Running more than one Adapter process against the same project with this
// coordinator reopens the double-claim race it exists to close; a
// multi-player or web deployment must supply a Coordinator backed by a
// shared durable store instead.
type InProcessCoordinator struct {
	mu sync.Mutex
}

// WithLock runs fn under the coordinator lock.
func (c *InProcessCoordinator) WithLock(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn()
}
