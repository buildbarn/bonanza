package label

import (
	"fmt"
	"strconv"
	"strings"
)

type queryTokenKind int

const (
	queryTokenEndOfInput queryTokenKind = iota
	queryTokenWord
	queryTokenQuotedWord
	queryTokenVariable
	queryTokenLeftParenthesis
	queryTokenRightParenthesis
	queryTokenComma
	queryTokenEquals
	queryTokenPlus
	queryTokenMinus
	queryTokenCaret
	queryTokenExcept
	queryTokenIn
	queryTokenIntersect
	queryTokenLet
	queryTokenSet
	queryTokenUnion
)

var queryKeywords = map[string]queryTokenKind{
	"except":    queryTokenExcept,
	"in":        queryTokenIn,
	"intersect": queryTokenIntersect,
	"let":       queryTokenLet,
	"set":       queryTokenSet,
	"union":     queryTokenUnion,
}

var queryPunctuation = map[byte]queryTokenKind{
	'(': queryTokenLeftParenthesis,
	')': queryTokenRightParenthesis,
	',': queryTokenComma,
	'=': queryTokenEquals,
	'+': queryTokenPlus,
	'-': queryTokenMinus,
	'^': queryTokenCaret,
}

type queryToken struct {
	kind queryTokenKind
	// For words, the contents of the word without any quotes. For
	// variables, the name of the variable without the leading "$".
	text string
	// Byte offset of the token within the input.
	offset int
}

func (t queryToken) String() string {
	switch t.kind {
	case queryTokenEndOfInput:
		return "end of query"
	case queryTokenQuotedWord:
		return fmt.Sprintf("word %#v", t.text)
	case queryTokenVariable:
		return "variable $" + t.text
	default:
		return fmt.Sprintf("%#v", t.text)
	}
}

// isQueryWordCharacter returns whether a character may be part of an
// unquoted word. Words starting with "@@" may also contain "+", as it
// is used by canonical repo names.
func isQueryWordCharacter(c byte, isCanonical bool) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		strings.IndexByte("*/@.-_:$~[]", c) >= 0 ||
		(c == '+' && isCanonical)
}

// isValidUnquotedQueryWord returns whether a string can be written as
// an unquoted word, without it being interpreted as a keyword or
// variable.
func isValidUnquotedQueryWord(s string) bool {
	if s == "" || s[0] == '-' || s[0] == '*' || s[0] == '$' {
		return false
	}
	if _, ok := queryKeywords[s]; ok {
		return false
	}
	isCanonical := strings.HasPrefix(s, "@@")
	for i := 0; i < len(s); i++ {
		if !isQueryWordCharacter(s[i], isCanonical) {
			return false
		}
	}
	return true
}

func tokenizeQuery(input string) ([]queryToken, error) {
	var tokens []queryToken
	for i := 0; ; {
		// Skip whitespace.
		for i < len(input) && strings.IndexByte(" \t\r\n", input[i]) >= 0 {
			i++
		}
		if i == len(input) {
			return append(tokens, queryToken{kind: queryTokenEndOfInput, offset: i}), nil
		}

		start := i
		c := input[i]
		if kind, ok := queryPunctuation[c]; ok {
			tokens = append(tokens, queryToken{kind: kind, text: input[i : i+1], offset: start})
			i++
			continue
		}

		if c == '"' || c == '\'' {
			// Quoted word. Quoted words don't support any
			// escaping.
			end := strings.IndexByte(input[i+1:], c)
			if end < 0 {
				return nil, fmt.Errorf("column %d: unterminated quoted word", start+1)
			}
			tokens = append(tokens, queryToken{kind: queryTokenQuotedWord, text: input[i+1 : i+1+end], offset: start})
			i += end + 2
			continue
		}

		// Unquoted word, keyword or variable. Words may not start
		// with "-" or "*".
		if c == '*' || !isQueryWordCharacter(c, false) {
			return nil, fmt.Errorf("column %d: unexpected character %#v", start+1, string(rune(c)))
		}
		isCanonical := strings.HasPrefix(input[i:], "@@")
		for i < len(input) && isQueryWordCharacter(input[i], isCanonical) {
			i++
		}
		word := input[start:i]
		if kind, ok := queryKeywords[word]; ok {
			tokens = append(tokens, queryToken{kind: kind, text: word, offset: start})
		} else if name, ok := strings.CutPrefix(word, "$"); ok {
			if name == "" {
				return nil, fmt.Errorf("column %d: variable name expected after \"$\"", start+1)
			}
			tokens = append(tokens, queryToken{kind: queryTokenVariable, text: name, offset: start})
		} else {
			tokens = append(tokens, queryToken{kind: queryTokenWord, text: word, offset: start})
		}
	}
}

// Functions that are part of the query language, but can only be used
// in combination with Sky Query, as they depend on the universe that
// is provided through --universe_scope. Calls to these functions yield
// a more descriptive error than calls to unknown functions.
var skyQueryFunctions = map[string]struct{}{
	"allrdeps":    {},
	"rbuildfiles": {},
}

type queryParser[TTargetPattern comparable] struct {
	tokens             []queryToken
	position           int
	builder            *queryBuilder[TTargetPattern]
	variables          map[string]int
	parseTargetPattern func(string) (TTargetPattern, error)
}

func (p *queryParser[TTargetPattern]) peek() queryToken {
	return p.tokens[p.position]
}

func (p *queryParser[TTargetPattern]) next() queryToken {
	t := p.tokens[p.position]
	if t.kind != queryTokenEndOfInput {
		p.position++
	}
	return t
}

func unexpectedQueryToken(t queryToken, expected string) error {
	return fmt.Errorf("column %d: expected %s, but found %s", t.offset+1, expected, t)
}

func (p *queryParser[TTargetPattern]) expect(kind queryTokenKind, expected string) error {
	if t := p.next(); t.kind != kind {
		return unexpectedQueryToken(t, expected)
	}
	return nil
}

// parseWord parses a word that is either quoted or unquoted.
func (p *queryParser[TTargetPattern]) parseWord(expected string) (string, error) {
	t := p.next()
	if t.kind != queryTokenWord && t.kind != queryTokenQuotedWord {
		return "", unexpectedQueryToken(t, expected)
	}
	return t.text, nil
}

func (p *queryParser[TTargetPattern]) parseTargetPatternToken(t queryToken) (TTargetPattern, error) {
	targetPattern, err := p.parseTargetPattern(t.text)
	if err != nil {
		var bad TTargetPattern
		return bad, fmt.Errorf("column %d: invalid target pattern %#v: %w", t.offset+1, t.text, err)
	}
	return targetPattern, nil
}

func (p *queryParser[TTargetPattern]) parseInteger(expected string) (uint32, queryToken, error) {
	t := p.next()
	if t.kind != queryTokenWord && t.kind != queryTokenQuotedWord {
		return 0, t, unexpectedQueryToken(t, expected)
	}
	v, err := strconv.ParseUint(t.text, 10, 32)
	if err != nil {
		return 0, t, fmt.Errorf("column %d: invalid %s %#v", t.offset+1, expected, t.text)
	}
	return uint32(v), t, nil
}

// parseOptionalInteger parses an optional trailing integer argument of
// a function call (e.g., the depth of deps()), followed by the closing
// parenthesis.
func (p *queryParser[TTargetPattern]) parseOptionalInteger(expected string) (*uint32, queryToken, error) {
	if p.peek().kind != queryTokenComma {
		return nil, queryToken{}, p.expect(queryTokenRightParenthesis, "\",\" or \")\"")
	}
	p.next()
	v, t, err := p.parseInteger(expected)
	if err != nil {
		return nil, t, err
	}
	return &v, t, p.expect(queryTokenRightParenthesis, "\")\"")
}

// parseArguments parses the mandatory arguments of a function call.
// Arguments are either words, which are stored in the provided
// strings, or expressions, whose indices are returned.
func (p *queryParser[TTargetPattern]) parseArguments(words []*string, expressionsCount int) ([]int, error) {
	for i, word := range words {
		if i > 0 {
			if err := p.expect(queryTokenComma, "\",\""); err != nil {
				return nil, err
			}
		}
		var err error
		if *word, err = p.parseWord("word"); err != nil {
			return nil, err
		}
	}
	expressions := make([]int, 0, expressionsCount)
	for i := range expressionsCount {
		if i > 0 || len(words) > 0 {
			if err := p.expect(queryTokenComma, "\",\""); err != nil {
				return nil, err
			}
		}
		expression, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		expressions = append(expressions, expression)
	}
	return expressions, nil
}

// parseCall parses the arguments of a function call, followed by the
// closing parenthesis.
func (p *queryParser[TTargetPattern]) parseCall(words []*string, expressionsCount int) ([]int, error) {
	expressions, err := p.parseArguments(words, expressionsCount)
	if err != nil {
		return nil, err
	}
	return expressions, p.expect(queryTokenRightParenthesis, "\")\"")
}

func (p *queryParser[TTargetPattern]) parseFunctionCall(name queryToken) (QueryExpression[TTargetPattern], error) {
	switch name.text {
	case "allpaths":
		expressions, err := p.parseCall(nil, 2)
		if err != nil {
			return nil, err
		}
		return &QueryAllPaths[TTargetPattern]{Start: expressions[0], End: expressions[1]}, nil
	case "attr":
		var attrName, pattern string
		expressions, err := p.parseCall([]*string{&attrName, &pattern}, 1)
		if err != nil {
			return nil, err
		}
		return &QueryAttr[TTargetPattern]{AttrName: attrName, Pattern: pattern, Expression: expressions[0]}, nil
	case "buildfiles":
		expressions, err := p.parseCall(nil, 1)
		if err != nil {
			return nil, err
		}
		return &QueryBuildFiles[TTargetPattern]{Expression: expressions[0]}, nil
	case "deps":
		expressions, err := p.parseArguments(nil, 1)
		if err != nil {
			return nil, err
		}
		maximumDepth, _, err := p.parseOptionalInteger("depth")
		if err != nil {
			return nil, err
		}
		return &QueryDeps[TTargetPattern]{Expression: expressions[0], MaximumDepth: maximumDepth}, nil
	case "executables":
		expressions, err := p.parseCall(nil, 1)
		if err != nil {
			return nil, err
		}
		return &QueryExecutables[TTargetPattern]{Expression: expressions[0]}, nil
	case "filter":
		var pattern string
		expressions, err := p.parseCall([]*string{&pattern}, 1)
		if err != nil {
			return nil, err
		}
		return &QueryFilter[TTargetPattern]{Pattern: pattern, Expression: expressions[0]}, nil
	case "kind":
		var pattern string
		expressions, err := p.parseCall([]*string{&pattern}, 1)
		if err != nil {
			return nil, err
		}
		return &QueryKind[TTargetPattern]{Pattern: pattern, Expression: expressions[0]}, nil
	case "labels":
		var attrName string
		expressions, err := p.parseCall([]*string{&attrName}, 1)
		if err != nil {
			return nil, err
		}
		return &QueryLabels[TTargetPattern]{AttrName: attrName, Expression: expressions[0]}, nil
	case "loadfiles":
		expressions, err := p.parseCall(nil, 1)
		if err != nil {
			return nil, err
		}
		return &QueryLoadFiles[TTargetPattern]{Expression: expressions[0]}, nil
	case "packages":
		expressions, err := p.parseCall(nil, 1)
		if err != nil {
			return nil, err
		}
		return &QueryPackages[TTargetPattern]{Expression: expressions[0]}, nil
	case "rdeps":
		expressions, err := p.parseArguments(nil, 2)
		if err != nil {
			return nil, err
		}
		maximumDepth, _, err := p.parseOptionalInteger("depth")
		if err != nil {
			return nil, err
		}
		return &QueryReverseDeps[TTargetPattern]{Universe: expressions[0], Expression: expressions[1], MaximumDepth: maximumDepth}, nil
	case "rules":
		expressions, err := p.parseCall(nil, 1)
		if err != nil {
			return nil, err
		}
		return &QueryRules[TTargetPattern]{Expression: expressions[0]}, nil
	case "same_pkg_direct_rdeps":
		expressions, err := p.parseCall(nil, 1)
		if err != nil {
			return nil, err
		}
		return &QuerySamePackageDirectReverseDeps[TTargetPattern]{Expression: expressions[0]}, nil
	case "siblings":
		expressions, err := p.parseCall(nil, 1)
		if err != nil {
			return nil, err
		}
		return &QuerySiblings[TTargetPattern]{Expression: expressions[0]}, nil
	case "some":
		expressions, err := p.parseArguments(nil, 1)
		if err != nil {
			return nil, err
		}
		count, countToken, err := p.parseOptionalInteger("count")
		if err != nil {
			return nil, err
		}
		if count == nil {
			return &QuerySome[TTargetPattern]{Expression: expressions[0], Count: 1}, nil
		}
		if *count == 0 {
			return nil, fmt.Errorf("column %d: count must be positive", countToken.offset+1)
		}
		return &QuerySome[TTargetPattern]{Expression: expressions[0], Count: *count}, nil
	case "somepath":
		expressions, err := p.parseCall(nil, 2)
		if err != nil {
			return nil, err
		}
		return &QuerySomePath[TTargetPattern]{Start: expressions[0], End: expressions[1]}, nil
	case "tag":
		var tag string
		expressions, err := p.parseCall([]*string{&tag}, 1)
		if err != nil {
			return nil, err
		}
		return &QueryTag[TTargetPattern]{Tag: tag, Expression: expressions[0]}, nil
	case "tests":
		expressions, err := p.parseCall(nil, 1)
		if err != nil {
			return nil, err
		}
		return &QueryTests[TTargetPattern]{Expression: expressions[0]}, nil
	case "visible":
		expressions, err := p.parseCall(nil, 2)
		if err != nil {
			return nil, err
		}
		return &QueryVisible[TTargetPattern]{Predicate: expressions[0], Expression: expressions[1]}, nil
	default:
		if _, ok := skyQueryFunctions[name.text]; ok {
			return nil, fmt.Errorf("column %d: function %#v requires Sky Query, which is not supported", name.offset+1, name.text)
		}
		return nil, fmt.Errorf("column %d: unknown function %#v", name.offset+1, name.text)
	}
}

// parsePrimary parses an expression that is not a set operation,
// returning its index.
func (p *queryParser[TTargetPattern]) parsePrimary() (int, error) {
	t := p.next()
	switch t.kind {
	case queryTokenWord:
		if p.peek().kind == queryTokenLeftParenthesis {
			p.next()
			expression, err := p.parseFunctionCall(t)
			if err != nil {
				return 0, err
			}
			return p.builder.add(expression), nil
		}
		fallthrough
	case queryTokenQuotedWord:
		targetPattern, err := p.parseTargetPatternToken(t)
		if err != nil {
			return 0, err
		}
		// Target patterns are sets containing a single element.
		return p.builder.add(&QuerySet[TTargetPattern]{TargetPatterns: []TTargetPattern{targetPattern}}), nil
	case queryTokenVariable:
		index, ok := p.variables[t.text]
		if !ok {
			return 0, fmt.Errorf("column %d: undefined variable $%s", t.offset+1, t.text)
		}
		return index, nil
	case queryTokenLeftParenthesis:
		expression, err := p.parseExpression()
		if err != nil {
			return 0, err
		}
		return expression, p.expect(queryTokenRightParenthesis, "\")\"")
	case queryTokenLet:
		// "let name = value in body". Variables refer to the
		// index of the expression they are bound to, meaning
		// they do not end up in the resulting query.
		nameToken := p.next()
		if nameToken.kind != queryTokenWord {
			return 0, unexpectedQueryToken(nameToken, "variable name")
		}
		if err := p.expect(queryTokenEquals, "\"=\""); err != nil {
			return 0, err
		}
		value, err := p.parseExpression()
		if err != nil {
			return 0, err
		}
		if err := p.expect(queryTokenIn, "\"in\""); err != nil {
			return 0, err
		}
		oldValue, hadOldValue := p.variables[nameToken.text]
		p.variables[nameToken.text] = value
		body, err := p.parseExpression()
		if hadOldValue {
			p.variables[nameToken.text] = oldValue
		} else {
			delete(p.variables, nameToken.text)
		}
		return body, err
	case queryTokenSet:
		if err := p.expect(queryTokenLeftParenthesis, "\"(\""); err != nil {
			return 0, err
		}
		var targetPatterns []TTargetPattern
		for {
			t := p.next()
			switch t.kind {
			case queryTokenWord, queryTokenQuotedWord:
				targetPattern, err := p.parseTargetPatternToken(t)
				if err != nil {
					return 0, err
				}
				targetPatterns = append(targetPatterns, targetPattern)
			case queryTokenRightParenthesis:
				return p.builder.add(&QuerySet[TTargetPattern]{TargetPatterns: targetPatterns}), nil
			default:
				return 0, unexpectedQueryToken(t, "word or \")\"")
			}
		}
	default:
		return 0, unexpectedQueryToken(t, "expression")
	}
}

var querySetOperators = map[queryTokenKind]QuerySetOperator{
	queryTokenPlus:      QuerySetOperatorUnion,
	queryTokenUnion:     QuerySetOperatorUnion,
	queryTokenCaret:     QuerySetOperatorIntersect,
	queryTokenIntersect: QuerySetOperatorIntersect,
	queryTokenMinus:     QuerySetOperatorExcept,
	queryTokenExcept:    QuerySetOperatorExcept,
}

// parseExpression parses an expression, returning its index.
func (p *queryParser[TTargetPattern]) parseExpression() (int, error) {
	// All set operators have the same precedence and are left
	// associative.
	left, err := p.parsePrimary()
	if err != nil {
		return 0, err
	}
	for {
		operator, ok := querySetOperators[p.peek().kind]
		if !ok {
			return left, nil
		}
		p.next()
		right, err := p.parsePrimary()
		if err != nil {
			return 0, err
		}
		left = p.builder.add(&QuerySetOperation[TTargetPattern]{Operator: operator, Left: left, Right: right})
	}
}

// NewQuery parses a query expression, using the language that is
// accepted by "bazel query". Target patterns contained in the query
// expression are parsed using the provided function. For example,
// NewCanonicalTargetPattern can be provided to parse a query expression
// containing canonical target patterns, while
// CanonicalPackage.AppendTargetPattern can be provided to parse a query
// expression containing target patterns that may be relative to the
// current package.
//
// The resulting query is in normal form.
func NewQuery[TTargetPattern interface {
	comparable
	fmt.Stringer
}](value string, parseTargetPattern func(string) (TTargetPattern, error)) (Query[TTargetPattern], error) {
	tokens, err := tokenizeQuery(value)
	if err != nil {
		return Query[TTargetPattern]{}, err
	}
	p := queryParser[TTargetPattern]{
		tokens:             tokens,
		builder:            newQueryBuilder[TTargetPattern](),
		variables:          map[string]int{},
		parseTargetPattern: parseTargetPattern,
	}
	root, err := p.parseExpression()
	if err != nil {
		return Query[TTargetPattern]{}, err
	}
	if t := p.peek(); t.kind != queryTokenEndOfInput {
		return Query[TTargetPattern]{}, unexpectedQueryToken(t, "end of query")
	}

	// Expressions are added to the builder in the order in which
	// they are parsed. Convert the query to normal form, which also
	// removes expressions of unused variables.
	return buildQuery(p.builder.expressions, root, func(targetPattern TTargetPattern) (TTargetPattern, error) {
		return targetPattern, nil
	}, compareQueryTargetPatterns[TTargetPattern], nil)
}
