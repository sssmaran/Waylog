package waylog

import "sync/atomic"

type stats struct {
	eventsEmitted   atomic.Uint64
	eventsDropped   atomic.Uint64
	validateFailed  atomic.Uint64
	transportErrors atomic.Uint64
}

func (s *stats) incEmitted(count uint64) {
	s.eventsEmitted.Add(count)
}

func (s *stats) incDropped(count uint64) {
	s.eventsDropped.Add(count)
}

func (s *stats) incValidateFailed(count uint64) {
	s.validateFailed.Add(count)
}

func (s *stats) incTransportErrors(count uint64) {
	s.transportErrors.Add(count)
}

func (s *stats) snapshot() StatsSnapshot {
	return StatsSnapshot{
		EventsEmitted:   s.eventsEmitted.Load(),
		EventsDropped:   s.eventsDropped.Load(),
		ValidateFailed:  s.validateFailed.Load(),
		TransportErrors: s.transportErrors.Load(),
	}
}
