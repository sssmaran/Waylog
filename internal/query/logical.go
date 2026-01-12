package query

import "github.com/sssmaran/WaylogCLI/internal/graph/store"

type AndPredicate struct {
	Left, Right Predicate
}

func (p AndPredicate) Eval(f store.RequestFacts) bool {
	return p.Left.Eval(f) && p.Right.Eval(f)
}

type OrPredicate struct {
	Left, Right Predicate
}

func (p OrPredicate) Eval(f store.RequestFacts) bool {
	return p.Left.Eval(f) || p.Right.Eval(f)
}
