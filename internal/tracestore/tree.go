package tracestore

import "sort"

type TreeNode struct {
	Span     SpanRecord
	Children []*TreeNode
}

// BuildTree reconstructs a span tree from a flat span list.
// Orphans and unresolved cycles are promoted to roots so callers always get a
// stable forest instead of losing evidence.
func BuildTree(spans []SpanRecord) []*TreeNode {
	if len(spans) == 0 {
		return nil
	}

	byID := make(map[string]SpanRecord, len(spans))
	order := make([]string, 0, len(spans))
	for _, span := range spans {
		if span.SpanID == "" {
			continue
		}
		if existing, ok := byID[span.SpanID]; ok {
			mergeSpanRecord(&existing, span)
			byID[span.SpanID] = existing
			continue
		}
		byID[span.SpanID] = span
		order = append(order, span.SpanID)
	}
	children := make(map[string][]string, len(spans))
	for _, span := range byID {
		if span.ParentSpanID != "" {
			children[span.ParentSpanID] = append(children[span.ParentSpanID], span.SpanID)
		}
	}

	sort.Strings(order)
	sortChildren := func(ids []string) {
		sort.Slice(ids, func(i, j int) bool {
			left := byID[ids[i]]
			right := byID[ids[j]]
			if left.Timestamp.Equal(right.Timestamp) {
				return left.SpanID < right.SpanID
			}
			if left.Timestamp.IsZero() {
				return false
			}
			if right.Timestamp.IsZero() {
				return true
			}
			return left.Timestamp.Before(right.Timestamp)
		})
	}
	for parentID := range children {
		sortChildren(children[parentID])
	}

	roots := make([]string, 0, len(order))
	for _, id := range order {
		span := byID[id]
		if span.ParentSpanID == "" {
			roots = append(roots, id)
			continue
		}
		if _, ok := byID[span.ParentSpanID]; !ok {
			roots = append(roots, id)
			continue
		}
	}
	if len(roots) == 0 {
		roots = append(roots, order...)
	}

	stack := make(map[string]bool, len(spans))
	var build func(string) *TreeNode
	build = func(id string) *TreeNode {
		span, ok := byID[id]
		if !ok {
			return nil
		}
		if stack[id] {
			return &TreeNode{Span: span}
		}
		stack[id] = true

		node := &TreeNode{Span: span}
		for _, childID := range children[id] {
			if stack[childID] {
				continue
			}
			if child := build(childID); child != nil {
				node.Children = append(node.Children, child)
			}
		}
		delete(stack, id)
		return node
	}

	out := make([]*TreeNode, 0, len(roots))
	for _, id := range roots {
		if node := build(id); node != nil {
			out = append(out, node)
		}
	}
	return out
}
