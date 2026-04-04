package query

import (
	"strconv"
	"strings"

	"github.com/sssmaran/WaylogCLI/internal/graph/store"
)

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
	case "error", "error_code":
		for _, e := range f.Errors {
			if e == p.Value {
				return true
			}
		}
	case "tier", "user_tier":
		return f.UserTier == p.Value
	case "user_id":
		return f.UserID == p.Value
	case "user_region":
		return f.UserRegion == p.Value
	case "user_vip":
		expected, err := strconv.ParseBool(strings.TrimSpace(p.Value))
		if err != nil {
			return false
		}
		return f.UserVIP == expected
	case "flag", "feature_flag":
		return f.HasFeatureFlag(p.Value)
	case "version":
		return f.Version == p.Value
	case "status":
		return f.Status == p.Value
	}
	return false
}
