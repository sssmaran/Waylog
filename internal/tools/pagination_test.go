package tools

import "testing"

func TestApplyPagination(t *testing.T) {
	items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	t.Run("first page", func(t *testing.T) {
		page, total, hasMore := applyPagination(items, 3, 0)
		if total != 10 {
			t.Errorf("total = %d, want 10", total)
		}
		if !hasMore {
			t.Error("expected has_more=true")
		}
		if len(page) != 3 {
			t.Errorf("len = %d, want 3", len(page))
		}
	})

	t.Run("middle page", func(t *testing.T) {
		page, _, hasMore := applyPagination(items, 3, 3)
		if !hasMore {
			t.Error("expected has_more=true")
		}
		if len(page) != 3 {
			t.Errorf("len = %d, want 3", len(page))
		}
		if page[0] != 4 {
			t.Errorf("first = %d, want 4", page[0])
		}
	})

	t.Run("last page", func(t *testing.T) {
		page, _, hasMore := applyPagination(items, 3, 9)
		if hasMore {
			t.Error("expected has_more=false")
		}
		if len(page) != 1 {
			t.Errorf("len = %d, want 1", len(page))
		}
	})

	t.Run("offset beyond length", func(t *testing.T) {
		page, total, hasMore := applyPagination(items, 3, 20)
		if hasMore {
			t.Error("expected has_more=false")
		}
		if len(page) != 0 {
			t.Errorf("len = %d, want 0", len(page))
		}
		if total != 10 {
			t.Errorf("total = %d, want 10", total)
		}
	})

	t.Run("zero limit uses default", func(t *testing.T) {
		page, _, _ := applyPagination(items, 0, 0)
		if len(page) != 10 {
			t.Errorf("len = %d, want 10 (default 100, clamped to len)", len(page))
		}
	})

	t.Run("negative offset treated as zero", func(t *testing.T) {
		page, total, hasMore := applyPagination(items, 3, -1)
		if total != 10 {
			t.Errorf("total = %d, want 10", total)
		}
		if !hasMore {
			t.Error("expected has_more=true")
		}
		if len(page) != 3 {
			t.Errorf("len = %d, want 3", len(page))
		}
		if page[0] != 1 {
			t.Errorf("first = %d, want 1", page[0])
		}
	})

	t.Run("limit exceeds max", func(t *testing.T) {
		big := make([]int, 2000)
		page, _, _ := applyPagination(big, 1500, 0)
		if len(page) != 1000 {
			t.Errorf("len = %d, want 1000 (max)", len(page))
		}
	})
}
