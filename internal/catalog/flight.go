package catalog

import (
	"context"
)

// flight is one scheduled load of a namespace. done is closed when the load
// finished; err is written before done is closed and read only afterwards.
type flight struct {
	done chan struct{}
	err  error
}

func newFlight() *flight {
	return &flight{done: make(chan struct{})}
}

// wait blocks until the load finished or ctx is done.
func (f *flight) wait(ctx context.Context) error {
	select {
	case <-f.done:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// nsFlights tracks the loads of one namespace. At most one load runs at a
// time (current); requests arriving while it runs share one queued load
// (next), which starts after current finished and therefore observes every
// change committed before those requests were made.
type nsFlights struct {
	current *flight
	next    *flight
}

// request schedules a load of namespaceID that starts after this call and
// returns it. The load runs on its own goroutine, detached from ctx
// cancellation (so that other waiters are not affected); its database work is
// bounded by the store's load timeout once a load slot is acquired. ctx values
// are preserved for the first load.
func (s *Store) request(ctx context.Context, namespaceID string) *flight {
	s.flightMu.Lock()
	defer s.flightMu.Unlock()
	st, ok := s.flights[namespaceID]
	if !ok {
		st = &nsFlights{}
		s.flights[namespaceID] = st
	}
	if st.next != nil {
		return st.next
	}
	f := newFlight()
	if st.current != nil {
		st.next = f
		return f
	}
	st.current = f
	// The shared load serves every waiter, so it must not be canceled with the
	// first requester; each load is bounded by the store's load timeout.
	go s.runFlights(context.WithoutCancel(ctx), namespaceID, st) //nolint:gosec // G118: detached on purpose, see above
	return f
}

// runFlights executes the current load and then any queued one, until no
// load is pending, and finally forgets the namespace's flight state.
func (s *Store) runFlights(base context.Context, namespaceID string, st *nsFlights) {
	for {
		s.flightMu.Lock()
		f := st.current
		s.flightMu.Unlock()

		f.err = s.load(base, namespaceID)
		close(f.done)

		s.flightMu.Lock()
		st.current, st.next = st.next, nil
		if st.current == nil {
			delete(s.flights, namespaceID)
			s.flightMu.Unlock()
			return
		}
		s.flightMu.Unlock()
		// Queued loads are not tied to the first requester's context values.
		base = context.Background()
	}
}
