package query

import (
	"fmt"
	"strings"
)

func Parse(expr string) (Predicate, error) {
	tokens := tokenize(expr)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty query")
	}
	p, rest, err := parseOr(tokens)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("unexpected tokens: %v", rest)
	}
	return p, nil
}

func tokenize(s string) []string {
	s = strings.ReplaceAll(s, "(", " ( ")
	s = strings.ReplaceAll(s, ")", " ) ")
	return strings.Fields(s)
}

// helper funcs
func parseOr(tokens []string) (Predicate, []string, error) {
	left, rest, err := parseAnd(tokens)
	if err != nil {
		return nil, nil, err
	}
	for len(rest) > 0 && strings.ToUpper(rest[0]) == "OR" {
		var right Predicate
		right, rest, err = parseAnd(rest[1:])
		if err != nil {
			return nil, nil, err
		}
		left = OrPredicate{Left: left, Right: right}
	}
	return left, rest, nil
}

func parseAnd(tokens []string) (Predicate, []string, error) {
	left, rest, err := parseAtom(tokens)
	if err != nil {
		return nil, nil, err
	}
	for len(rest) > 0 && strings.ToUpper(rest[0]) == "AND" {
		var right Predicate
		right, rest, err = parseAtom(rest[1:])
		if err != nil {
			return nil, nil, err
		}
		left = AndPredicate{Left: left, Right: right}
	}
	return left, rest, nil
}

func parseAtom(tokens []string) (Predicate, []string, error) {
	if tokens[0] == "(" {
		p, rest, err := parseOr(tokens[1:])
		if err != nil {
			return nil, nil, err
		}
		if len(rest) == 0 || rest[0] != ")" {
			return nil, nil, fmt.Errorf("missing )")
		}
		return p, rest[1:], nil
	}

	parts := strings.Split(tokens[0], "=")
	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("invalid expression: %s", tokens[0])
	}
	return EqualsPredicate{
		Field: parts[0],
		Value: parts[1],
	}, tokens[1:], nil
}
