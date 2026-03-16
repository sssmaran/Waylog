package tools

// applyPagination slices a sorted list by offset/limit and returns
// pagination metadata. Default limit: 100, max limit: 1000.
func applyPagination[T any](items []T, limit, offset int) (page []T, totalCount int, hasMore bool) {
	totalCount = len(items)

	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	if offset < 0 {
		offset = 0
	}
	if offset >= totalCount {
		return []T{}, totalCount, false
	}

	end := offset + limit
	if end > totalCount {
		end = totalCount
	}

	return items[offset:end], totalCount, end < totalCount
}
