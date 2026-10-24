package label

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Query is a parsed query expression, using the language that is
// accepted by "bazel query". Query expressions evaluate to sets of
// targets.
//
// Queries are stored as a list of expressions, where expressions refer
// to other expressions by index. This allows expressions that are
// referenced multiple times (e.g., through the use of "let") to be
// stored only once. Identical expressions are always deduplicated.
// The list of expressions is in normal form, meaning that queries that
// are equivalent from a syntactic point of view are identical.
//
// Queries are parameterized by the type of target pattern that they
// contain, so that queries containing apparent and canonical target
// patterns cannot be mixed up.
//
// More details on the query language can be found at
// https://bazel.build/query/language.
type Query[TTargetPattern comparable] struct {
	expressions []QueryExpression[TTargetPattern]
}

// GetExpressions returns the expressions that make up the query.
// Expressions only refer to expressions that precede them. The last
// expression is the top-level expression of the query. The caller
// MUST NOT modify the resulting list.
func (q Query[TTargetPattern]) GetExpressions() []QueryExpression[TTargetPattern] {
	return q.expressions
}

// GetSubquery returns a query whose top-level expression is the
// expression at a given index. This can be used to evaluate
// subexpressions separately.
func (q Query[TTargetPattern]) GetSubquery(index int) Query[TTargetPattern] {
	// The query is already in normal form, meaning there is no
	// need to sort the contents of sets.
	subquery, err := buildQuery(q.expressions, index, func(targetPattern TTargetPattern) (TTargetPattern, error) {
		return targetPattern, nil
	}, nil)
	if err != nil {
		panic(err)
	}
	return subquery
}

// QueryExpression is an expression contained in a query. Expressions
// that have operands refer to them by their index in the list of
// expressions of the query.
type QueryExpression[TTargetPattern any] interface {
	writeTo(f *queryFormatter[TTargetPattern], precedence queryPrecedence)
}

// CanonicalizeQuery converts a query containing apparent target
// patterns to one containing canonical target patterns, by calling
// Canonicalize() on each of them. It fails if any of the target
// patterns reference an unknown apparent repo.
//
// As the contents of sets are sorted by their string representation,
// canonical target patterns need to be convertible to strings.
func CanonicalizeQuery[
	TCanonical interface {
		comparable
		fmt.Stringer
	},
	TApparent interface {
		comparable
		Canonicalizable[TCanonical]
	},
](resolver Resolver, fromRepo CanonicalRepo, toApparent Query[TApparent]) (Query[TCanonical], error) {
	return buildQuery(toApparent.expressions, len(toApparent.expressions)-1, func(targetPattern TApparent) (TCanonical, error) {
		return Canonicalize(resolver, fromRepo, targetPattern)
	}, compareQueryTargetPatterns[TCanonical])
}

// compareQueryTargetPatterns compares target patterns by their string
// representation. It is used to sort the contents of sets.
func compareQueryTargetPatterns[TTargetPattern fmt.Stringer](a, b TTargetPattern) int {
	return strings.Compare(a.String(), b.String())
}

// QuerySet is a query expression that yields the union of the targets
// matched by zero or more target patterns. Sets containing a single
// target pattern are written as just the target pattern (e.g.,
// "//foo:bar" or "//foo/..."). Other sets are written as "set(a b c)".
//
// In normal form, the target patterns are sorted and deduplicated.
type QuerySet[TTargetPattern any] struct {
	TargetPatterns []TTargetPattern
}

func (e *QuerySet[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	if len(e.TargetPatterns) == 1 {
		f.writeTargetPattern(e.TargetPatterns[0])
		return
	}
	f.WriteString("set(")
	for i, targetPattern := range e.TargetPatterns {
		if i > 0 {
			f.WriteByte(' ')
		}
		f.writeTargetPattern(targetPattern)
	}
	f.WriteByte(')')
}

// QuerySetOperator is the operator of a QuerySetOperation.
type QuerySetOperator int

const (
	// QuerySetOperatorUnion yields targets that are present in
	// either operand, written as "x + y" or "x union y".
	QuerySetOperatorUnion QuerySetOperator = iota
	// QuerySetOperatorIntersect yields targets that are present in
	// both operands, written as "x ^ y" or "x intersect y".
	QuerySetOperatorIntersect
	// QuerySetOperatorExcept yields targets that are present in the
	// left operand, but not in the right operand, written as
	// "x - y" or "x except y".
	QuerySetOperatorExcept
)

// QuerySetOperation is a query expression that applies a set operation
// to the results of two query expressions. All set operators have the
// same precedence and are left associative.
type QuerySetOperation[TTargetPattern any] struct {
	Operator QuerySetOperator
	// Indices of the operands in the list of expressions.
	Left, Right int
}

func (e *QuerySetOperation[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], precedence queryPrecedence) {
	if precedence > queryPrecedenceSetOperation {
		f.WriteByte('(')
		defer f.WriteByte(')')
	}
	f.writeExpression(e.Left, queryPrecedenceSetOperation)
	switch e.Operator {
	case QuerySetOperatorUnion:
		f.WriteString(" + ")
	case QuerySetOperatorIntersect:
		f.WriteString(" ^ ")
	case QuerySetOperatorExcept:
		f.WriteString(" - ")
	default:
		panic("unknown set operator")
	}
	f.writeExpression(e.Right, queryPrecedencePrimary)
}

// QueryDeps is a query expression that yields the transitive closure
// of the dependencies of a set of targets, written as "deps(x)" or
// "deps(x, depth)".
type QueryDeps[TTargetPattern any] struct {
	// Index of the operand in the list of expressions.
	Expression int
	// If set, the maximum number of edges to traverse. A depth of
	// zero yields the original set of targets.
	MaximumDepth *uint32
}

func (e *QueryDeps[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("deps", nil, []int{e.Expression}, e.MaximumDepth)
}

// QueryReverseDeps is a query expression that yields the targets
// within the transitive closure of a universe that transitively depend
// on a set of targets, written as "rdeps(u, x)" or
// "rdeps(u, x, depth)".
type QueryReverseDeps[TTargetPattern any] struct {
	// Indices of the operands in the list of expressions.
	Universe   int
	Expression int
	// If set, the maximum number of edges to traverse. A depth of
	// zero yields the original set of targets.
	MaximumDepth *uint32
}

func (e *QueryReverseDeps[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("rdeps", nil, []int{e.Universe, e.Expression}, e.MaximumDepth)
}

// QueryKind is a query expression that yields the targets of a set
// whose kind (e.g., "cc_library rule", "source file") matches an
// unanchored regular expression, written as "kind(pattern, x)".
type QueryKind[TTargetPattern any] struct {
	Pattern string
	// Index of the operand in the list of expressions.
	Expression int
}

func (e *QueryKind[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("kind", []string{e.Pattern}, []int{e.Expression}, nil)
}

// QueryFilter is a query expression that yields the targets of a set
// whose label matches an unanchored regular expression, written as
// "filter(pattern, x)".
type QueryFilter[TTargetPattern any] struct {
	Pattern string
	// Index of the operand in the list of expressions.
	Expression int
}

func (e *QueryFilter[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("filter", []string{e.Pattern}, []int{e.Expression}, nil)
}

// QueryAttr is a query expression that yields the rule targets of a
// set having an attribute whose value matches an unanchored regular
// expression, written as "attr(name, pattern, x)".
type QueryAttr[TTargetPattern any] struct {
	AttrName string
	Pattern  string
	// Index of the operand in the list of expressions.
	Expression int
}

func (e *QueryAttr[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("attr", []string{e.AttrName, e.Pattern}, []int{e.Expression}, nil)
}

// QueryLabels is a query expression that yields the targets referenced
// by an attribute of the rule targets in a set, written as
// "labels(name, x)".
type QueryLabels[TTargetPattern any] struct {
	AttrName string
	// Index of the operand in the list of expressions.
	Expression int
}

func (e *QueryLabels[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("labels", []string{e.AttrName}, []int{e.Expression}, nil)
}

// QueryTests is a query expression that yields the test rules in a
// set, with test suites expanded into the tests they contain, written
// as "tests(x)".
type QueryTests[TTargetPattern any] struct {
	// Index of the operand in the list of expressions.
	Expression int
}

func (e *QueryTests[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("tests", nil, []int{e.Expression}, nil)
}

// QuerySome is a query expression that yields an arbitrary subset of
// a set, written as "some(x)" or "some(x, count)". It is an error for
// the set to be empty.
type QuerySome[TTargetPattern any] struct {
	// Index of the operand in the list of expressions.
	Expression int
	// The maximum number of targets to select, which is positive.
	Count uint32
}

func (e *QuerySome[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	// A count of one is the default, and is omitted.
	var count *uint32
	if e.Count != 1 {
		count = &e.Count
	}
	f.writeCall("some", nil, []int{e.Expression}, count)
}

// QuerySiblings is a query expression that yields all targets in the
// packages of the targets in a set, written as "siblings(x)".
type QuerySiblings[TTargetPattern any] struct {
	// Index of the operand in the list of expressions.
	Expression int
}

func (e *QuerySiblings[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("siblings", nil, []int{e.Expression}, nil)
}

// QuerySomePath is a query expression that yields the targets on an
// arbitrary path from any target in a set of start targets to any
// target in a set of end targets, following dependency edges, written
// as "somepath(s, e)".
type QuerySomePath[TTargetPattern any] struct {
	// Indices of the operands in the list of expressions.
	Start int
	End   int
}

func (e *QuerySomePath[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("somepath", nil, []int{e.Start, e.End}, nil)
}

// QueryAllPaths is a query expression that yields the targets on all
// paths from any target in a set of start targets to any target in a
// set of end targets, following dependency edges, written as
// "allpaths(s, e)".
type QueryAllPaths[TTargetPattern any] struct {
	// Indices of the operands in the list of expressions.
	Start int
	End   int
}

func (e *QueryAllPaths[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("allpaths", nil, []int{e.Start, e.End}, nil)
}

// QueryVisible is a query expression that yields the targets of a set
// that are visible to all targets in a set of predicate targets,
// written as "visible(predicate, x)".
type QueryVisible[TTargetPattern any] struct {
	// Indices of the operands in the list of expressions.
	Predicate  int
	Expression int
}

func (e *QueryVisible[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("visible", nil, []int{e.Predicate, e.Expression}, nil)
}

// QuerySamePackageDirectReverseDeps is a query expression that yields
// the targets that directly depend on a target in a set, and are in the
// same package as that target, written as "same_pkg_direct_rdeps(x)".
type QuerySamePackageDirectReverseDeps[TTargetPattern any] struct {
	// Index of the operand in the list of expressions.
	Expression int
}

func (e *QuerySamePackageDirectReverseDeps[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("same_pkg_direct_rdeps", nil, []int{e.Expression}, nil)
}

// QueryExecutables is a query expression that yields the executable
// targets in a set, written as "executables(x)".
type QueryExecutables[TTargetPattern any] struct {
	// Index of the operand in the list of expressions.
	Expression int
}

func (e *QueryExecutables[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("executables", nil, []int{e.Expression}, nil)
}

// QueryBuildFiles is a query expression that yields the BUILD files of
// the packages of the targets in a set, and the .bzl files loaded by
// them, written as "buildfiles(x)".
type QueryBuildFiles[TTargetPattern any] struct {
	// Index of the operand in the list of expressions.
	Expression int
}

func (e *QueryBuildFiles[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("buildfiles", nil, []int{e.Expression}, nil)
}

// QueryLoadFiles is a query expression that yields the .bzl files that
// are loaded by the BUILD files of the packages of the targets in a
// set, written as "loadfiles(x)".
type QueryLoadFiles[TTargetPattern any] struct {
	// Index of the operand in the list of expressions.
	Expression int
}

func (e *QueryLoadFiles[TTargetPattern]) writeTo(f *queryFormatter[TTargetPattern], _ queryPrecedence) {
	f.writeCall("loadfiles", nil, []int{e.Expression}, nil)
}

// queryPrecedence indicates which kinds of query expressions may be
// written without enclosing them in parentheses.
type queryPrecedence int

const (
	// Any query expression may be written without parentheses,
	// such as the argument of a function call.
	queryPrecedenceSetOperation queryPrecedence = iota
	// Set operations need to be enclosed in parentheses, such as
	// the right operand of a set operation. As set operators are
	// left associative and have the same precedence, the left
	// operand does not need parentheses.
	queryPrecedencePrimary
)

// queryFormatter is used to convert query expressions to strings. It
// is used both to convert queries to strings in normal form, and to
// compute keys of individual expressions for deduplication.
type queryFormatter[TTargetPattern any] struct {
	strings.Builder
	writeTargetPattern func(targetPattern TTargetPattern)
	writeExpression    func(index int, precedence queryPrecedence)
}

// writeCall writes a function call, whose arguments consist of words,
// followed by expressions, followed by an optional integer.
func (f *queryFormatter[TTargetPattern]) writeCall(name string, words []string, expressions []int, integer *uint32) {
	f.WriteString(name)
	f.WriteByte('(')
	for i, word := range words {
		if i > 0 {
			f.WriteString(", ")
		}
		f.WriteString(quoteQueryWord(word))
	}
	for i, expression := range expressions {
		if i > 0 || len(words) > 0 {
			f.WriteString(", ")
		}
		f.writeExpression(expression, queryPrecedenceSetOperation)
	}
	if integer != nil {
		f.WriteString(", ")
		f.WriteString(strconv.FormatUint(uint64(*integer), 10))
	}
	f.WriteByte(')')
}

// QueryToString converts a query to a string in normal form. Parsing the
// resulting string yields an identical query. As queries that are
// equivalent from a syntactic point of view have the same normal form,
// the resulting string can be used as part of evaluation keys.
//
// Expressions that are referenced multiple times are declared as
// variables using "let". All other expressions are written inline.
func QueryToString[TTargetPattern interface {
	comparable
	fmt.Stringer
}](q Query[TTargetPattern]) string {
	// Determine which expressions are referenced multiple times.
	referenceCounts := make([]int, len(q.expressions))
	countingFormatter := queryFormatter[TTargetPattern]{
		writeTargetPattern: func(TTargetPattern) {},
		writeExpression: func(index int, _ queryPrecedence) {
			referenceCounts[index]++
		},
	}
	for _, expression := range q.expressions {
		expression.writeTo(&countingFormatter, queryPrecedenceSetOperation)
	}

	// Assign variable names to expressions in the order in which
	// they appear in the list.
	variableNames := make([]string, len(q.expressions))
	var variables []int
	for i, referenceCount := range referenceCounts {
		if referenceCount > 1 {
			variableNames[i] = "v" + strconv.FormatInt(int64(len(variables)), 10)
			variables = append(variables, i)
		}
	}

	var f queryFormatter[TTargetPattern]
	f.writeTargetPattern = func(targetPattern TTargetPattern) {
		f.WriteString(quoteQueryWord(targetPattern.String()))
	}
	f.writeExpression = func(index int, precedence queryPrecedence) {
		if name := variableNames[index]; name != "" {
			f.WriteString("$" + name)
		} else {
			q.expressions[index].writeTo(&f, precedence)
		}
	}
	for _, variable := range variables {
		f.WriteString("let " + variableNames[variable] + " = ")
		q.expressions[variable].writeTo(&f, queryPrecedenceSetOperation)
		f.WriteString(" in ")
	}
	q.expressions[len(q.expressions)-1].writeTo(&f, queryPrecedenceSetOperation)
	return f.String()
}

// queryBuilder is used to construct the list of expressions of a
// query, deduplicating identical expressions.
type queryBuilder[TTargetPattern comparable] struct {
	expressions          []QueryExpression[TTargetPattern]
	expressionIndices    map[string]int
	targetPatternIndices map[TTargetPattern]int
}

func newQueryBuilder[TTargetPattern comparable]() *queryBuilder[TTargetPattern] {
	return &queryBuilder[TTargetPattern]{
		expressionIndices:    map[string]int{},
		targetPatternIndices: map[TTargetPattern]int{},
	}
}

// add an expression to the query, returning its index. If an identical
// expression already exists, the index of the existing expression is
// returned.
func (b *queryBuilder[TTargetPattern]) add(expression QueryExpression[TTargetPattern]) int {
	// Compute a key for the expression by converting it to a
	// string. Operands are already deduplicated, meaning they can
	// be referenced by index. Target patterns are referenced by
	// index, as they may not be convertible to strings.
	var f queryFormatter[TTargetPattern]
	f.writeTargetPattern = func(targetPattern TTargetPattern) {
		index, ok := b.targetPatternIndices[targetPattern]
		if !ok {
			index = len(b.targetPatternIndices)
			b.targetPatternIndices[targetPattern] = index
		}
		f.WriteString("#" + strconv.FormatInt(int64(index), 10))
	}
	f.writeExpression = func(index int, _ queryPrecedence) {
		f.WriteString("$" + strconv.FormatInt(int64(index), 10))
	}
	expression.writeTo(&f, queryPrecedenceSetOperation)
	key := f.String()

	if index, ok := b.expressionIndices[key]; ok {
		return index
	}
	index := len(b.expressions)
	b.expressions = append(b.expressions, expression)
	b.expressionIndices[key] = index
	return index
}

// buildQuery constructs a query in normal form, containing the
// expressions that are reachable from a given top-level expression.
// Target patterns contained in the expressions are converted using
// the provided function.
//
// In normal form, the contents of sets are sorted and deduplicated,
// identical expressions are deduplicated, and expressions are stored in
// the order in which they are first encountered in a depth-first,
// left-to-right traversal of the top-level expression, with operands
// preceding their users.
//
// The contents of sets are sorted using the provided comparison
// function. It may be nil if the contents of sets are known to already
// be sorted and deduplicated.
func buildQuery[TFrom, TTo comparable](expressions []QueryExpression[TFrom], root int, convertTargetPattern func(TFrom) (TTo, error), compareTargetPatterns func(a, b TTo) int) (Query[TTo], error) {
	b := newQueryBuilder[TTo]()
	if err := convertQueryExpressions(b, expressions, root, convertTargetPattern, compareTargetPatterns); err != nil {
		return Query[TTo]{}, err
	}

	// Converting target patterns and sorting the contents of sets
	// may have caused expressions to become identical. Because
	// these are deduplicated, the order of the remaining
	// expressions may not match the order of a traversal. Perform
	// another pass to fix up the order.
	if len(b.expressions) < len(expressions) {
		b2 := newQueryBuilder[TTo]()
		if err := convertQueryExpressions(b2, b.expressions, len(b.expressions)-1, func(targetPattern TTo) (TTo, error) {
			return targetPattern, nil
		}, nil); err != nil {
			return Query[TTo]{}, err
		}
		b = b2
	}
	return Query[TTo]{expressions: b.expressions}, nil
}

// convertQueryExpressions adds expressions that are reachable from a
// top-level expression to a query builder, converting the target
// patterns contained within. If a comparison function is provided, the
// contents of sets are sorted and deduplicated.
func convertQueryExpressions[TFrom, TTo comparable](b *queryBuilder[TTo], expressions []QueryExpression[TFrom], root int, convertTargetPattern func(TFrom) (TTo, error), compareTargetPatterns func(a, b TTo) int) error {
	newIndices := map[int]int{}
	var visit func(index int) (int, error)
	visit = func(index int) (int, error) {
		if newIndex, ok := newIndices[index]; ok {
			return newIndex, nil
		}
		var newExpression QueryExpression[TTo]
		switch e := expressions[index].(type) {
		case *QuerySet[TFrom]:
			targetPatterns := make([]TTo, 0, len(e.TargetPatterns))
			for _, targetPattern := range e.TargetPatterns {
				newTargetPattern, err := convertTargetPattern(targetPattern)
				if err != nil {
					return 0, err
				}
				targetPatterns = append(targetPatterns, newTargetPattern)
			}
			if compareTargetPatterns != nil {
				// Sets are unordered, and may contain
				// duplicate target patterns.
				slices.SortFunc(targetPatterns, compareTargetPatterns)
				targetPatterns = slices.Compact(targetPatterns)
			}
			newExpression = &QuerySet[TTo]{TargetPatterns: targetPatterns}
		case *QuerySetOperation[TFrom]:
			left, err := visit(e.Left)
			if err != nil {
				return 0, err
			}
			right, err := visit(e.Right)
			if err != nil {
				return 0, err
			}
			newExpression = &QuerySetOperation[TTo]{Operator: e.Operator, Left: left, Right: right}
		case *QueryDeps[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryDeps[TTo]{Expression: expression, MaximumDepth: e.MaximumDepth}
		case *QueryReverseDeps[TFrom]:
			universe, err := visit(e.Universe)
			if err != nil {
				return 0, err
			}
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryReverseDeps[TTo]{Universe: universe, Expression: expression, MaximumDepth: e.MaximumDepth}
		case *QueryKind[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryKind[TTo]{Pattern: e.Pattern, Expression: expression}
		case *QueryFilter[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryFilter[TTo]{Pattern: e.Pattern, Expression: expression}
		case *QueryAttr[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryAttr[TTo]{AttrName: e.AttrName, Pattern: e.Pattern, Expression: expression}
		case *QueryLabels[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryLabels[TTo]{AttrName: e.AttrName, Expression: expression}
		case *QueryTests[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryTests[TTo]{Expression: expression}
		case *QuerySome[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QuerySome[TTo]{Expression: expression, Count: e.Count}
		case *QuerySiblings[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QuerySiblings[TTo]{Expression: expression}
		case *QuerySomePath[TFrom]:
			start, err := visit(e.Start)
			if err != nil {
				return 0, err
			}
			end, err := visit(e.End)
			if err != nil {
				return 0, err
			}
			newExpression = &QuerySomePath[TTo]{Start: start, End: end}
		case *QueryAllPaths[TFrom]:
			start, err := visit(e.Start)
			if err != nil {
				return 0, err
			}
			end, err := visit(e.End)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryAllPaths[TTo]{Start: start, End: end}
		case *QueryVisible[TFrom]:
			predicate, err := visit(e.Predicate)
			if err != nil {
				return 0, err
			}
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryVisible[TTo]{Predicate: predicate, Expression: expression}
		case *QuerySamePackageDirectReverseDeps[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QuerySamePackageDirectReverseDeps[TTo]{Expression: expression}
		case *QueryExecutables[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryExecutables[TTo]{Expression: expression}
		case *QueryBuildFiles[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryBuildFiles[TTo]{Expression: expression}
		case *QueryLoadFiles[TFrom]:
			expression, err := visit(e.Expression)
			if err != nil {
				return 0, err
			}
			newExpression = &QueryLoadFiles[TTo]{Expression: expression}
		default:
			panic("unknown query expression type")
		}
		newIndex := b.add(newExpression)
		newIndices[index] = newIndex
		return newIndex, nil
	}
	_, err := visit(root)
	return err
}

// quoteQueryWord converts a string to a word of the query language,
// adding quotes if needed.
func quoteQueryWord(s string) string {
	if isValidUnquotedQueryWord(s) {
		return s
	}
	if strings.IndexByte(s, '"') >= 0 {
		return "'" + s + "'"
	}
	return "\"" + s + "\""
}
