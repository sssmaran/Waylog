package query

import "github.com/sssmaran/WaylogCLI/internal/graph/store"

type Predicate interface {
	Eval(f store.RequestFacts) bool
}
