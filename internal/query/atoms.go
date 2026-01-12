package query

import "github.com/sssmaran/WaylogCLI/internal/graph/store"

type EqualsPredicate struct {
	Field string
	Value string
}

func (p EqualsPredicate) Eval(f store.RequestFacts) bool {
	switch p.Field {
	case "service":
		for _, s := range f.Services {
			if s == p.Value {
				return true
			}
		}
	case "error":
		for _, e := range f.Errors {
			if e == p.Value {
				return true
			}
		}
	case "flag":
		for _, fl := range f.Flags {
			if fl == p.Value {
				return true
			}
		}
	case "tier":
		return f.Tier == p.Value
	case "version":
		return f.Version == p.Value
	case "status":
		return f.Status == p.Value
	}
	return false
}
