package memory

import (
	"slices"

	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// InboundRows returns a copy of the rows the store holds, in insert order.
func (s *Records) InboundRows() []app.InboundEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.inbound)
}
