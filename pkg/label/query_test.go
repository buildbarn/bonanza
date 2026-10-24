package label_test

import (
	"errors"
	"testing"

	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestNewQuery(t *testing.T) {
	t.Run("NormalForm", func(t *testing.T) {
		for input, expected := range map[string]string{
			// Target patterns, and words that need quoting.
			"@@a+//foo:bar":            "@@a+//foo:bar",
			"  @@a+//foo/...  ":        "@@a+//foo/...",
			"@@a+//foo:foo":            "@@a+//foo",
			"@@a+//foo:all-targets":    "@@a+//foo:all-targets",
			"@@gazelle++go_deps+x//:*": "@@gazelle++go_deps+x//:*",
			`"@@a+//foo:bar baz"`:      `"@@a+//foo:bar baz"`,
			`'@@a+//foo:a"b'`:          `'@@a+//foo:a"b'`,
			`"@@a+//foo:a=b"`:          `"@@a+//foo:a=b"`,

			// Set operations. All operators have the same
			// precedence and are left associative.
			"@@a+//x union @@a+//y":                    "@@a+//x + @@a+//y",
			"@@a+//x except @@a+//y intersect @@a+//z": "@@a+//x - @@a+//y ^ @@a+//z",
			"(@@a+//x - @@a+//y) ^ @@a+//z":            "@@a+//x - @@a+//y ^ @@a+//z",
			"@@a+//x - (@@a+//y ^ @@a+//z)":            "@@a+//x - (@@a+//y ^ @@a+//z)",
			"((@@a+//x))":                              "@@a+//x",
			"set()":                                    "set()",
			"set(@@a+//x '@@a+//y:a b'   @@a+//y:z)":   "set(@@a+//x \"@@a+//y:a b\" @@a+//y:z)",

			// The contents of sets are sorted and
			// deduplicated, as sets are unordered.
			"set(@@a+//z @@a+//x @@a+//y @@a+//x)": "set(@@a+//x @@a+//y @@a+//z)",
			"set(@@a+//x:x @@a+//x)":               "@@a+//x",

			// Sets containing a single target pattern are
			// equivalent to the target pattern itself, and are
			// written as such.
			"set(@@a+//x)":                                "@@a+//x",
			"set(@@a+//x) + @@a+//x":                      "let v0 = @@a+//x in $v0 + $v0",
			"deps(set('@@a+//x:a b'))":                    "deps(\"@@a+//x:a b\")",
			"set(@@a+//x @@a+//y) - set(@@a+//y @@a+//x)": "let v0 = set(@@a+//x @@a+//y) in $v0 - $v0",

			// Functions.
			"deps(@@a+//x)":                          "deps(@@a+//x)",
			"deps(@@a+//x,0)":                        "deps(@@a+//x, 0)",
			"rdeps(@@a+//..., @@a+//x + @@a+//y, 1)": "rdeps(@@a+//..., @@a+//x + @@a+//y, 1)",
			"kind('cc_.* rule', @@a+//...)":          `kind("cc_.* rule", @@a+//...)`,
			"kind(source, @@a+//...)":                "kind(source, @@a+//...)",
			`filter('\.go$', @@a+//...)`:             `filter("\.go$", @@a+//...)`,
			"attr(linkshared, 1, @@a+//...)":         "attr(linkshared, 1, @@a+//...)",
			"labels(srcs, @@a+//x)":                  "labels(srcs, @@a+//x)",
			"tests(@@a+//...)":                       "tests(@@a+//...)",
			"some(@@a+//...)":                        "some(@@a+//...)",
			"some(@@a+//..., 1)":                     "some(@@a+//...)",
			"some(@@a+//..., 3)":                     "some(@@a+//..., 3)",
			"siblings(@@a+//x)":                      "siblings(@@a+//x)",
			"somepath(@@a+//x, @@a+//y)":             "somepath(@@a+//x, @@a+//y)",
			"allpaths(@@a+//x,@@a+//y)":              "allpaths(@@a+//x, @@a+//y)",
			"visible(@@a+//x, @@a+//...)":            "visible(@@a+//x, @@a+//...)",
			"same_pkg_direct_rdeps(@@a+//x)":         "same_pkg_direct_rdeps(@@a+//x)",
			"executables(@@a+//...)":                 "executables(@@a+//...)",
			"buildfiles(deps(@@a+//x))":              "buildfiles(deps(@@a+//x))",
			"loadfiles(@@a+//x)":                     "loadfiles(@@a+//x)",
			"tag(manual, @@a+//...)":                 "tag(manual, @@a+//...)",
			"tag('a b', @@a+//...)":                  `tag("a b", @@a+//...)`,
			"packages(@@a+//...)":                    "packages(@@a+//...)",
			"rules(@@a+//x:*)":                       "rules(@@a+//x:*)",

			// Expressions that are used once are written
			// inline. Expressions that are used multiple times
			// are declared as variables, using names that are
			// assigned in order.
			"let v = @@a+//x in deps($v) - $v":                   "let v0 = @@a+//x in deps($v0) - $v0",
			"let v = @@a+//x in let w = $v + @@a+//y in $w - $v": "let v0 = @@a+//x in $v0 + @@a+//y - $v0",
			"let v = @@a+//x in (let v = @@a+//y in $v) + $v":    "@@a+//y + @@a+//x",
			"let v = @@a+//x in @@a+//y + $v":                    "@@a+//y + @@a+//x",
			"let v = set(@@a+//x @@a+//y) in deps($v) - $v":      "let v0 = set(@@a+//x @@a+//y) in deps($v0) - $v0",

			// Identical target patterns are merged as well.
			"@@a+//x + @@a+//x": "let v0 = @@a+//x in $v0 + $v0",

			// Unused variables are removed.
			"let v = deps(@@a+//x) in @@a+//y": "@@a+//y",

			"let foo = deps(@@a+//x) in $foo - $foo":                                "let v0 = deps(@@a+//x) in $v0 - $v0",
			"let a = deps(@@a+//x) in let b = rdeps(@@a+//..., $a) in $b + $b - $a": "let v0 = deps(@@a+//x) in let v1 = rdeps(@@a+//..., $v0) in $v1 + $v1 - $v0",
			"let a = @@a+//x in let b = $a + $a in let c = $b + $b in $c ^ $c":      "let v0 = @@a+//x in let v1 = $v0 + $v0 in let v2 = $v1 + $v1 in $v2 ^ $v2",
			"let a = deps(@@a+//x) in @@a+//y - ($a + $a)":                          "let v0 = deps(@@a+//x) in @@a+//y - ($v0 + $v0)",

			// Identical subexpressions are merged, even if
			// they are not declared as variables.
			"deps(@@a+//x) + deps(@@a+//x)":             "let v0 = deps(@@a+//x) in $v0 + $v0",
			"deps(@@a+//x:x) + deps(@@a+//x)":           "let v0 = deps(@@a+//x) in $v0 + $v0",
			"kind(rule, @@a+//x) + kind(rule, @@a+//y)": "kind(rule, @@a+//x) + kind(rule, @@a+//y)",
		} {
			t.Run(input, func(t *testing.T) {
				q, err := label.NewQuery(input, label.NewCanonicalTargetPattern)
				require.NoError(t, err)
				require.Equal(t, expected, label.QueryToString(q))

				// The normal form should be a fixed point.
				q2, err := label.NewQuery(label.QueryToString(q), label.NewCanonicalTargetPattern)
				require.NoError(t, err)
				require.Equal(t, q, q2)
			})
		}
	})

	t.Run("Failure", func(t *testing.T) {
		for input, expected := range map[string]string{
			"":                                  "column 1: expected expression, but found end of query",
			"@@a+//x +":                         "column 10: expected expression, but found end of query",
			"@@a+//x @@a+//y":                   "column 9: expected end of query, but found \"@@a+//y\"",
			"(@@a+//x":                          "column 9: expected \")\", but found end of query",
			"'@@a+//x":                          "column 1: unterminated quoted word",
			"@@a+//x = @@a+//y":                 "column 9: expected end of query, but found \"=\"",
			"*":                                 "column 1: unexpected character \"*\"",
			"@@a+//x & @@a+//y":                 "column 9: unexpected character \"&\"",
			"$v":                                "column 1: undefined variable $v",
			"(let v = @@a+//x in @@a+//y) + $v": "column 32: undefined variable $v",
			"let v = @@a+//x $v":                "column 17: expected \"in\", but found variable $v",
			"let v = @@a+//x in $w":             "column 20: undefined variable $w",
			"deps(@@a+//x, y)":                  "column 15: invalid depth \"y\"",
			"deps(@@a+//x, 1, 2)":               "column 16: expected \")\", but found \",\"",
			"kind(@@a+//x)":                     "column 13: expected \",\", but found \")\"",
			"tag(@@a+//x)":                      "column 12: expected \",\", but found \")\"",
			"some(@@a+//x, 0)":                  "column 15: count must be positive",
			"allrdeps(@@a+//x)":                 "column 1: function \"allrdeps\" requires Sky Query, which is not supported",
			"rbuildfiles(foo/BUILD)":            "column 1: function \"rbuildfiles\" requires Sky Query, which is not supported",
			"somepath(@@a+//x)":                 "column 17: expected \",\", but found \")\"",
			"visible(@@a+//x, @@a+//y, 1)":      "column 25: expected \")\", but found \",\"",
			"foo(@@a+//x)":                      "column 1: unknown function \"foo\"",
			"set(@@a+//x + @@a+//y)":            "column 13: expected word or \")\", but found \"+\"",
		} {
			t.Run(input, func(t *testing.T) {
				_, err := label.NewQuery(input, label.NewCanonicalTargetPattern)
				require.EqualError(t, err, expected)
			})
		}
	})

	t.Run("UnspacedUnionOfCanonicalTargetPatterns", func(t *testing.T) {
		// Words starting with "@@" may contain "+", as it is used
		// by canonical repo names. Unions of such words thus
		// need to be separated by whitespace.
		_, err := label.NewQuery("@@a+//x+@@a+//y", label.NewCanonicalTargetPattern)
		require.ErrorContains(t, err, "column 1: invalid target pattern \"@@a+//x+@@a+//y\"")
	})

	t.Run("NonCanonicalTargetPattern", func(t *testing.T) {
		_, err := label.NewQuery("deps(@@a+//x) + @rules_go//go/...", label.NewCanonicalTargetPattern)
		require.ErrorContains(t, err, "column 17: invalid target pattern \"@rules_go//go/...\"")
	})
}

func TestQueryStructure(t *testing.T) {
	q := util.Must(label.NewQuery("let v = @@a+//x in rdeps(@@a+//..., $v, 2) - $v", label.NewCanonicalTargetPattern))

	// Expressions are stored in the order in which they are first
	// encountered, with operands preceding their users. The
	// top-level expression is stored last. Identical expressions
	// are stored once.
	expressions := q.GetExpressions()
	require.Len(t, expressions, 4)
	setOperation, ok := expressions[3].(*label.QuerySetOperation[label.CanonicalTargetPattern])
	require.True(t, ok)
	require.Equal(t, label.QuerySetOperatorExcept, setOperation.Operator)
	require.Equal(t, 2, setOperation.Left)
	require.Equal(t, 1, setOperation.Right)

	reverseDeps, ok := expressions[2].(*label.QueryReverseDeps[label.CanonicalTargetPattern])
	require.True(t, ok)
	require.Equal(t, 0, reverseDeps.Universe)
	require.Equal(t, 1, reverseDeps.Expression)
	require.Equal(t, uint32(2), *reverseDeps.MaximumDepth)

	// Subqueries can be extracted, so that they can be evaluated
	// separately. They are in normal form.
	require.Equal(t, "@@a+//...", label.QueryToString(q.GetSubquery(0)))
	require.Equal(t, "@@a+//x", label.QueryToString(q.GetSubquery(1)))
	require.Equal(t, "rdeps(@@a+//..., @@a+//x, 2)", label.QueryToString(q.GetSubquery(2)))
	require.Equal(t, util.Must(label.NewQuery("rdeps(@@a+//..., @@a+//x, 2)", label.NewCanonicalTargetPattern)), q.GetSubquery(2))
	require.Equal(t, q, q.GetSubquery(3))
}

func TestNewQueryRelativeTargetPatterns(t *testing.T) {
	canonicalPackage := util.Must(label.NewCanonicalPackage("@@main+//foo"))

	t.Run("Success", func(t *testing.T) {
		q, err := label.NewQuery("deps(:bar + bar/... + //baz + @rules_go//go:def.bzl) - set(qux @@x+//y)", canonicalPackage.AppendTargetPattern)
		require.NoError(t, err)
		require.Equal(t, "deps(@@main+//foo:bar + @@main+//foo/bar/... + @@main+//baz + @rules_go//go:def.bzl) - set(@@main+//foo:qux @@x+//y)", label.QueryToString(q))

		// The result is a valid apparent query expression.
		q2, err := label.NewQuery(label.QueryToString(q), label.NewApparentTargetPattern)
		require.NoError(t, err)
		require.Equal(t, q, q2)
	})

	t.Run("InvalidTargetPattern", func(t *testing.T) {
		_, err := label.NewQuery("deps(':a:b')", canonicalPackage.AppendTargetPattern)
		require.ErrorContains(t, err, "column 6: invalid target pattern \":a:b\"")
	})
}

type fakeResolver struct{}

func (fakeResolver) GetCanonicalRepo(fromCanonicalRepo label.CanonicalRepo, toApparentRepo label.ApparentRepo) (*label.CanonicalRepo, error) {
	switch toApparentRepo.String() {
	case "rules_go":
		canonicalRepo := util.Must(label.NewCanonicalRepo("rules_go+"))
		return &canonicalRepo, nil
	case "a", "b":
		canonicalRepo := util.Must(label.NewCanonicalRepo("bla+"))
		return &canonicalRepo, nil
	case "broken":
		return nil, errors.New("repo mapping unavailable")
	default:
		return nil, nil
	}
}

func (fakeResolver) GetRootModule() (label.Module, error) {
	return util.Must(label.NewModule("main")), nil
}

func TestCanonicalizeQuery(t *testing.T) {
	fromRepo := util.Must(label.NewCanonicalRepo("main+"))

	t.Run("Success", func(t *testing.T) {
		q, err := label.CanonicalizeQuery(
			fakeResolver{},
			fromRepo,
			util.Must(label.NewQuery("deps(@rules_go//go/...) - set(@@//:a @@x+//y)", label.NewApparentTargetPattern)),
		)
		require.NoError(t, err)
		require.Equal(t, "deps(@@rules_go+//go/...) - set(@@main+//:a @@x+//y)", label.QueryToString(q))
	})

	t.Run("MergeIdenticalExpressions", func(t *testing.T) {
		// Expressions that only differ in whether they use
		// apparent or canonical repo names become identical.
		q, err := label.CanonicalizeQuery(
			fakeResolver{},
			fromRepo,
			util.Must(label.NewQuery("deps(@rules_go//go) + deps(@@rules_go+//go) - kind(rule, @rules_go//go)", label.NewApparentTargetPattern)),
		)
		require.NoError(t, err)
		require.Equal(t, "let v0 = @@rules_go+//go in let v1 = deps($v0) in $v1 + $v1 - kind(rule, $v0)", label.QueryToString(q))
		require.Equal(t, util.Must(label.NewQuery(label.QueryToString(q), label.NewCanonicalTargetPattern)), q)
	})

	t.Run("MergeApparentReposMappingToSameCanonicalRepo", func(t *testing.T) {
		// Multiple apparent repos may map to the same canonical
		// repo. This causes both operands to become identical,
		// meaning they are stored only once. As the sets only
		// contain a single target pattern, they are written as
		// just the target pattern.
		q, err := label.CanonicalizeQuery(
			fakeResolver{},
			fromRepo,
			util.Must(label.NewQuery("set(@a//x) - set(@b//x)", label.NewApparentTargetPattern)),
		)
		require.NoError(t, err)

		expressions := q.GetExpressions()
		require.Len(t, expressions, 2)
		require.Equal(t, &label.QuerySet[label.CanonicalTargetPattern]{
			TargetPatterns: []label.CanonicalTargetPattern{
				util.Must(label.NewCanonicalTargetPattern("@@bla+//x")),
			},
		}, expressions[0])
		require.Equal(t, &label.QuerySetOperation[label.CanonicalTargetPattern]{
			Operator: label.QuerySetOperatorExcept,
			Left:     0,
			Right:    0,
		}, expressions[1])

		require.Equal(t, "let v0 = @@bla+//x in $v0 - $v0", label.QueryToString(q))
		require.Equal(t, util.Must(label.NewQuery(label.QueryToString(q), label.NewCanonicalTargetPattern)), q)
	})

	t.Run("SortSetsAfterCanonicalization", func(t *testing.T) {
		// Sets that only differ in the order of their contents
		// after canonicalization become identical.
		q, err := label.CanonicalizeQuery(
			fakeResolver{},
			fromRepo,
			util.Must(label.NewQuery("set(@a//x @rules_go//go) - set(@@rules_go+//go @b//x)", label.NewApparentTargetPattern)),
		)
		require.NoError(t, err)
		require.Equal(t, "let v0 = set(@@bla+//x @@rules_go+//go) in $v0 - $v0", label.QueryToString(q))
		require.Len(t, q.GetExpressions(), 2)
		require.Equal(t, util.Must(label.NewQuery(label.QueryToString(q), label.NewCanonicalTargetPattern)), q)
	})

	t.Run("DeduplicateSetsAfterCanonicalization", func(t *testing.T) {
		// Both sets contain the same target pattern twice after
		// canonicalization. They are deduplicated, causing both
		// sets to become identical and to only contain a single
		// target pattern.
		q, err := label.CanonicalizeQuery(
			fakeResolver{},
			fromRepo,
			util.Must(label.NewQuery("set(@a//x @b//x) - set(@b//x @a//x)", label.NewApparentTargetPattern)),
		)
		require.NoError(t, err)
		require.Equal(t, "let v0 = @@bla+//x in $v0 - $v0", label.QueryToString(q))
		require.Len(t, q.GetExpressions(), 2)
	})

	t.Run("UnknownRepo", func(t *testing.T) {
		_, err := label.CanonicalizeQuery(
			fakeResolver{},
			fromRepo,
			util.Must(label.NewQuery("@rules_go//go + @unknown//foo", label.NewApparentTargetPattern)),
		)
		require.EqualError(t, err, "unknown apparent repo @unknown referenced by repo @@main+")
	})

	t.Run("ResolverFailure", func(t *testing.T) {
		_, err := label.CanonicalizeQuery(
			fakeResolver{},
			fromRepo,
			util.Must(label.NewQuery("kind(rule, @broken//foo)", label.NewApparentTargetPattern)),
		)
		require.EqualError(t, err, "failed to resolve apparent repo \"broken\": repo mapping unavailable")
	})
}

func TestMapQueryTargetPatterns(t *testing.T) {
	replace := func(query string, replacements map[string]string) string {
		q, err := label.MapQueryTargetPatterns(
			util.Must(label.NewQuery(query, label.NewCanonicalTargetPattern)),
			func(targetPattern label.CanonicalTargetPattern) (label.CanonicalTargetPattern, error) {
				if replacement, ok := replacements[targetPattern.String()]; ok {
					return label.NewCanonicalTargetPattern(replacement)
				}
				return targetPattern, nil
			},
		)
		require.NoError(t, err)
		return label.QueryToString(q)
	}

	t.Run("MergeExpressions", func(t *testing.T) {
		// Replacing target patterns may cause expressions to
		// become identical.
		require.Equal(
			t,
			"let v0 = kind(rule, @@a+//z) in $v0 + $v0",
			replace("kind(rule, @@a+//x) + kind(rule, @@a+//y)", map[string]string{
				"@@a+//x": "@@a+//z",
				"@@a+//y": "@@a+//z",
			}),
		)
	})

	t.Run("SortSets", func(t *testing.T) {
		// Sets should remain sorted and deduplicated.
		require.Equal(
			t,
			"set(@@a+//a @@a+//y)",
			replace("set(@@a+//x @@a+//y @@a+//z)", map[string]string{
				"@@a+//x": "@@a+//y",
				"@@a+//z": "@@a+//a",
			}),
		)
	})
}

func TestCombineQueries(t *testing.T) {
	combine := func(left, right string) string {
		q := label.CombineQueries(
			label.QuerySetOperatorExcept,
			util.Must(label.NewQuery(left, label.NewCanonicalTargetPattern)),
			util.Must(label.NewQuery(right, label.NewCanonicalTargetPattern)),
		)

		// The resulting query should be in normal form.
		s := label.QueryToString(q)
		require.Equal(t, util.Must(label.NewQuery(s, label.NewCanonicalTargetPattern)), q)
		return s
	}

	require.Equal(t, "@@a+//x/... - @@a+//x/y/...", combine("@@a+//x/...", "@@a+//x/y/..."))
	require.Equal(t, "@@a+//x/... - @@a+//x/y/... - @@a+//x/z/...", combine("@@a+//x/... - @@a+//x/y/...", "@@a+//x/z/..."))

	// Expressions that are shared between both queries should be
	// merged.
	require.Equal(
		t,
		"let v0 = @@a+//x/... in $v0 - tag(manual, $v0) - $v0",
		combine("@@a+//x/... - tag(manual, @@a+//x/...)", "@@a+//x/..."),
	)
}

func TestReplaceQuerySets(t *testing.T) {
	replace := func(query string, replacements map[string]string) string {
		q, err := label.ReplaceQuerySets(
			util.Must(label.NewQuery(query, label.NewCanonicalTargetPattern)),
			func(targetPatterns []label.CanonicalTargetPattern) (label.Query[label.CanonicalTargetPattern], bool) {
				if len(targetPatterns) == 1 {
					if replacement, ok := replacements[targetPatterns[0].String()]; ok {
						return util.Must(label.NewQuery(replacement, label.NewCanonicalTargetPattern)), true
					}
				}
				return label.Query[label.CanonicalTargetPattern]{}, false
			},
		)
		require.NoError(t, err)

		// The resulting query should be in normal form.
		s := label.QueryToString(q)
		require.Equal(t, util.Must(label.NewQuery(s, label.NewCanonicalTargetPattern)), q)
		return s
	}

	t.Run("SharedOperand", func(t *testing.T) {
		// Replacements of sets that are used multiple times
		// should be shared.
		require.Equal(
			t,
			"let v0 = rules(siblings(@@a+//x:*)) in $v0 - tag(manual, $v0)",
			replace("@@a+//x/... - tag(manual, @@a+//x/...)", map[string]string{
				"@@a+//x/...": "rules(siblings(@@a+//x:*))",
			}),
		)
	})

	t.Run("MergeExpressions", func(t *testing.T) {
		// Replacing sets may cause expressions to become
		// identical, including ones contained in the
		// replacements.
		require.Equal(
			t,
			"let v0 = siblings(@@a+//z:*) in let v1 = kind(rule, $v0) in $v1 + $v1 - $v0",
			replace("kind(rule, @@a+//x) + kind(rule, @@a+//y) - @@a+//w", map[string]string{
				"@@a+//w": "siblings(@@a+//z:*)",
				"@@a+//x": "siblings(@@a+//z:*)",
				"@@a+//y": "siblings(@@a+//z:*)",
			}),
		)
	})

	t.Run("KeepSets", func(t *testing.T) {
		// Sets that are not replaced should be retained.
		require.Equal(
			t,
			"set(@@a+//x @@a+//y) + packages(@@a+//z:*)",
			replace("set(@@a+//y @@a+//x) + packages(@@a+//z)", map[string]string{
				"@@a+//z": "@@a+//z:*",
			}),
		)
	})
}

func TestNewQueryFromTargetPatternArguments(t *testing.T) {
	newQuery := func(arguments []string, tagFilters ...string) (string, error) {
		query, err := label.NewQueryFromTargetPatternArguments(arguments, tagFilters, label.NewCanonicalTargetPattern)
		if err != nil {
			return "", err
		}
		return label.QueryToString(query), nil
	}

	t.Run("Success", func(t *testing.T) {
		for _, testCase := range []struct {
			arguments  []string
			tagFilters []string
			query      string
		}{
			// No target patterns yield no targets.
			{nil, nil, "set()"},
			{nil, []string{"a"}, "set()"},
			// Target patterns referring to a single target
			// match it, even if it has tag "manual".
			{[]string{"@@a+//x"}, nil, "@@a+//x"},
			{[]string{"@@a+//x", "@@a+//y"}, nil, "@@a+//x + @@a+//y"},
			// Target patterns containing wildcards exclude
			// targets having tag "manual".
			{[]string{"@@a+//..."}, nil, "let v0 = @@a+//... in $v0 - tag(manual, $v0)"},
			// Target patterns prefixed with "-" remove
			// targets from the results of the target
			// patterns preceding them. There is no need to
			// filter the targets they match.
			{[]string{"@@a+//x", "-@@a+//y"}, nil, "@@a+//x - @@a+//y"},
			{[]string{"@@a+//...", "-@@a+//b/..."}, nil, "let v0 = @@a+//... in $v0 - tag(manual, $v0) - @@a+//b/..."},
			{[]string{"@@a+//x", "-@@a+//y", "@@a+//z"}, nil, "@@a+//x - @@a+//y + @@a+//z"},
			// Removing targets from an empty set is a no-op.
			{[]string{"-@@a+//x", "@@a+//y"}, nil, "@@a+//y"},
			{[]string{"-@@a+//x"}, nil, "set()"},
			// Tag filters are applied to each target
			// pattern individually. Their order does not
			// affect the resulting query, and empty tag
			// filters are ignored.
			{[]string{"@@a+//x"}, []string{"", "-"}, "@@a+//x"},
			{[]string{"@@a+//x"}, []string{"b", "a", "b"}, "let v0 = @@a+//x in tag(a, $v0) + tag(b, $v0)"},
			{[]string{"@@a+//x"}, []string{"-d", "-c"}, "let v0 = @@a+//x in $v0 - tag(c, $v0) - tag(d, $v0)"},
			{
				[]string{"@@a+//...", "-@@a+//b:all", "@@a+//c"},
				[]string{"a", "-b"},
				"let v0 = @@a+//... in let v1 = @@a+//c in tag(a, $v0) - tag(b, $v0) - tag(manual, $v0) - @@a+//b:all + (tag(a, $v1) - tag(b, $v1))",
			},
			// An explicit "-manual" tag filter should not
			// cause the tag to be excluded twice.
			{[]string{"@@a+//..."}, []string{"-manual"}, "let v0 = @@a+//... in $v0 - tag(manual, $v0)"},
		} {
			query, err := newQuery(testCase.arguments, testCase.tagFilters...)
			require.NoError(t, err, testCase.arguments)
			require.Equal(t, testCase.query, query, testCase.arguments)
		}
	})

	t.Run("InvalidTargetPattern", func(t *testing.T) {
		_, err := newQuery([]string{"@@a+//x", "-foo"})
		require.ErrorContains(t, err, "invalid target pattern \"-foo\": ")
	})
}
