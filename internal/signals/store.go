package signals

import (
	"context"
	"errors"
	"time"
)

var ErrUnavailable = errors.New("signals: store unavailable")

type Store interface {
	Insert(ctx context.Context, s *Signal) error
	Query(ctx context.Context, f Filter) ([]Signal, error)
	PruneOlderThan(ctx context.Context, cutoff time.Time) (int, error)
}

type Filter struct {
	Service string
	Env     string
	Source  string
	Reason  string
	Types   []Type
	Since   time.Time
	Until   time.Time
	Limit   int
}

type UnavailableStore struct{}

func (UnavailableStore) Insert(context.Context, *Signal) error {
	return ErrUnavailable
}

func (UnavailableStore) Query(context.Context, Filter) ([]Signal, error) {
	return nil, ErrUnavailable
}

func (UnavailableStore) PruneOlderThan(context.Context, time.Time) (int, error) {
	return 0, nil
}
