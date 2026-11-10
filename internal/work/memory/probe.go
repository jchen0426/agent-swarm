package memory

import (
	"context"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Probe is the memory stores' app.Probe: each item's version as the Postgres
// probe reads it (design D2; R138), its records read under the records
// store's lock and its run's board under the event store's.
type Probe struct {
	records *Records
	events  *Store
}

// NewProbe returns the probe of the items records keeps, whose runs' boards
// events keeps.
func NewProbe(records *Records, events *Store) *Probe {
	return &Probe{records: records, events: events}
}

// Versions gives each item's latest record, the run its latest run_created
// names, and that run's latest entry, as the Postgres probe's statement does;
// an item with no record is absent.
func (p *Probe) Versions(_ context.Context, items []item.ID) (map[item.ID]app.BoardVersion, error) {
	out := make(map[item.ID]app.BoardVersion, len(items))
	p.records.mu.Lock()
	for _, id := range items {
		records := p.records.records[id]
		n := len(records)
		if n == 0 {
			continue
		}
		v := app.BoardVersion{Record: app.Mark{ID: records[n-1].ID, Seq: records[n-1].Seq}}
		for i := n - 1; i >= 0; i-- {
			if records[i].Body.Kind() == item.KindRunCreated {
				v.Run = records[i].Run
				break
			}
		}
		out[id] = v
	}
	p.records.mu.Unlock()
	p.events.mu.Lock()
	defer p.events.mu.Unlock()
	for id, v := range out {
		if events := p.events.events[v.Run]; v.Run != "" && len(events) > 0 {
			last := &events[len(events)-1]
			v.Board = app.Mark{ID: string(last.ID), Seq: last.Seq}
			out[id] = v
		}
	}
	return out, nil
}
