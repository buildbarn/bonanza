package syntax

// Options that control which syntax is accepted by the parser.
type Options struct {
	// TypeSyntax enables syntax for type annotations, as described
	// in https://github.com/bazelbuild/starlark/pull/362. This
	// includes annotations of parameters, return values and
	// variables, type aliases ("type X = ..."), type parameters,
	// and the cast() and isinstance() keywords.
	TypeSyntax bool
}

// Parse a Starlark source file.
func Parse(filename string, src []byte, options Options) (f *File, err error) {
	p := newParser(filename, src, &options)
	defer p.recover(&err)
	p.nextToken()

	var stmts []Stmt
	for p.tok != EOF {
		if p.tok == NEWLINE {
			p.nextToken()
			continue
		}
		stmts = p.parseStmt(stmts)
	}
	return &File{Path: filename, Stmts: stmts}, nil
}

// ParseExpr parses a single Starlark expression. A comma separated
// list of expressions is parsed as a tuple.
func ParseExpr(filename string, src []byte, options Options) (x Expr, err error) {
	p := newParser(filename, src, &options)
	defer p.recover(&err)
	p.nextToken()

	x = p.parseExpr(false)
	if p.tok == NEWLINE {
		p.nextToken()
	}
	if p.tok != EOF {
		p.errorf(p.tokval.pos, "got %#v after expression, want end of file", p.tok)
	}
	return x, nil
}

// parser of Starlark source files. It is a recursive descent parser
// for a grammar that is LL(1), except for type alias statements,
// which require one token of additional lookahead.
const (
	// maximumNestingDepth limits the depth of recursion of the
	// parser. Exceeding the maximum size of a goroutine's stack
	// terminates the process, which can't be recovered from.
	maximumNestingDepth = 1000

	// maximumTreeDepth limits the depth of the resulting syntax
	// tree. Chains like "a + b + c" and "a.b.c" are parsed
	// iteratively, but yield trees that are as deep as they are
	// long. Stages like the resolver and the compiler process these
	// trees recursively.
	maximumTreeDepth = 100000
)

type parser struct {
	sc     *scanner
	tok    Token
	tokval tokenValue

	// Token following the current one, if it has been peeked.
	hasPeeked bool
	peekTok   Token
	peekVal   tokenValue

	// Current depth of recursion of the parser.
	nestingDepth int
	// Upper bound of the depth in the syntax tree of the node that
	// is currently being parsed, and the highest value it has
	// reached.
	treeDepth    int
	maxTreeDepth int
}

func newParser(filename string, src []byte, options *Options) *parser {
	return &parser{sc: newScanner(filename, src, options)}
}

// recover converts syntax errors reported by the scanner and parser
// to errors. It must be called using defer.
func (*parser) recover(err *error) {
	switch e := recover().(type) {
	case nil:
	case *Error:
		*err = e
	default:
		panic(e)
	}
}

func (p *parser) errorf(pos Position, format string, args ...any) {
	p.sc.errorf(pos, format, args...)
}

func (p *parser) setTreeDepth(depth int) {
	if depth > maximumTreeDepth {
		p.errorf(p.tokval.pos, "code is nested too deeply, as it exceeds the maximum syntax tree depth of %d", maximumTreeDepth)
	}
	p.treeDepth = depth
	p.maxTreeDepth = max(p.maxTreeDepth, depth)
}

// enterNesting needs to be called by functions that are part of a cycle
// in the parser's call graph. It must be paired with a deferred call to
// leaveNesting().
func (p *parser) enterNesting() {
	if p.nestingDepth == maximumNestingDepth {
		p.errorf(p.tokval.pos, "code is nested too deeply, as it exceeds the maximum nesting depth of %d", maximumNestingDepth)
	}
	p.nestingDepth++
	p.setTreeDepth(p.treeDepth + 1)
}

func (p *parser) leaveNesting() {
	p.nestingDepth--
	p.treeDepth--
}

// nodeChain tracks the depth of the syntax tree while parsing a chain
// of nodes in a loop, where each node has the previous one as a child.
// As the first node ends up at the bottom of the tree, the depth of the
// chain consists of the height of the first node, plus the number of
// nodes that follow.
type nodeChain struct {
	start        int
	maxTreeDepth int
	length       int
}

// beginChain needs to be called before parsing the first node of a
// chain. It must be paired with a deferred call to endChain().
func (p *parser) beginChain() nodeChain {
	c := nodeChain{
		start:        p.treeDepth,
		maxTreeDepth: p.maxTreeDepth,
	}
	p.maxTreeDepth = p.treeDepth
	return c
}

// extendChain needs to be called before parsing any subsequent nodes of
// a chain.
func (p *parser) extendChain(c *nodeChain) {
	if c.length == 0 {
		c.length = p.maxTreeDepth - c.start
	}
	c.length++
	p.setTreeDepth(c.start + c.length)
}

func (p *parser) endChain(c *nodeChain) {
	p.treeDepth = c.start
	p.maxTreeDepth = max(p.maxTreeDepth, c.maxTreeDepth)
}

// nextToken advances to the next token, returning the position of the
// current one.
func (p *parser) nextToken() Position {
	pos := p.tokval.pos
	if p.hasPeeked {
		p.tok, p.tokval = p.peekTok, p.peekVal
		p.hasPeeked = false
	} else {
		p.tok = p.sc.nextToken(&p.tokval)
	}
	return pos
}

// peek returns the token following the current one.
func (p *parser) peek() Token {
	if !p.hasPeeked {
		p.peekTok = p.sc.nextToken(&p.peekVal)
		p.hasPeeked = true
	}
	return p.peekTok
}

// consume checks that the current token is of the expected type, and
// advances to the next token.
func (p *parser) consume(t Token) Position {
	if p.tok != t {
		p.errorf(p.tokval.pos, "got %#v, want %#v", p.tok, t)
	}
	return p.nextToken()
}

func (p *parser) checkTypeSyntax(what string) {
	if !p.sc.options.TypeSyntax {
		p.errorf(p.tokval.pos, "%s are not permitted, as type syntax is not enabled", what)
	}
}

// stmt = def_stmt | if_stmt | for_stmt | while_stmt | simple_stmt
func (p *parser) parseStmt(stmts []Stmt) []Stmt {
	switch p.tok {
	case DEF:
		return append(stmts, p.parseDefStmt())
	case IF:
		return append(stmts, p.parseIfStmt())
	case FOR:
		return append(stmts, p.parseForStmt())
	case WHILE:
		p.errorf(p.tokval.pos, "while statements are not permitted")
	}
	return p.parseSimpleStmt(stmts)
}

// def_stmt = DEF IDENT [type_params] '(' params ')' ['->' type_expr] ':' suite
func (p *parser) parseDefStmt() Stmt {
	def := p.nextToken()
	name := p.parseIdent()
	var typeParams []*Ident
	if p.tok == LBRACK {
		p.checkTypeSyntax("type parameters")
		typeParams = p.parseTypeParams()
	}
	lparen := p.consume(LPAREN)
	params := p.parseParams(true)
	rparen := p.consume(RPAREN)
	var returnType Expr
	if p.tok == ARROW {
		p.checkTypeSyntax("return type annotations")
		p.nextToken()
		returnType = p.parseTypeExpr()
	}
	p.consume(COLON)
	body := p.parseSuite()
	return &DefStmt{
		Def:        def,
		Name:       name,
		TypeParams: typeParams,
		Lparen:     lparen,
		Params:     params,
		Rparen:     rparen,
		ReturnType: returnType,
		Body:       body,
	}
}

// type_params = '[' IDENT {',' IDENT} ']'
func (p *parser) parseTypeParams() []*Ident {
	p.consume(LBRACK)
	params := []*Ident{p.parseIdent()}
	for p.tok == COMMA {
		p.nextToken()
		params = append(params, p.parseIdent())
	}
	p.consume(RBRACK)
	return params
}

// if_stmt = IF test ':' suite {ELIF test ':' suite} [ELSE ':' suite]
func (p *parser) parseIfStmt() Stmt {
	chain := p.beginChain()
	defer p.endChain(&chain)

	ifPos := p.nextToken()
	cond := p.parseTest()
	p.consume(COLON)
	ifStmt := &IfStmt{
		If:   ifPos,
		Cond: cond,
		True: p.parseSuite(),
	}
	tail := ifStmt
	for p.tok == ELIF {
		p.extendChain(&chain)
		elifPos := p.nextToken()
		cond := p.parseTest()
		p.consume(COLON)
		elif := &IfStmt{
			If:   elifPos,
			Cond: cond,
			True: p.parseSuite(),
		}
		tail.ElsePos = elifPos
		tail.False = []Stmt{elif}
		tail = elif
	}
	if p.tok == ELSE {
		tail.ElsePos = p.nextToken()
		p.consume(COLON)
		tail.False = p.parseSuite()
	}
	return ifStmt
}

// for_stmt = FOR loop_variables IN expr ':' suite
func (p *parser) parseForStmt() Stmt {
	forPos := p.nextToken()
	vars := p.parseForLoopVariables()
	p.consume(IN)
	x := p.parseExpr(false)
	p.consume(COLON)
	return &ForStmt{
		For:  forPos,
		Vars: vars,
		X:    x,
		Body: p.parseSuite(),
	}
}

// loop_variables = primary_with_suffix {',' primary_with_suffix} [',']
//
// This production is used instead of expr, as that would consume the
// IN token in "for x in y".
func (p *parser) parseForLoopVariables() Expr {
	v := p.parsePrimaryWithSuffix()
	if p.tok != COMMA {
		return v
	}
	list := []Expr{v}
	for p.tok == COMMA {
		p.nextToken()
		if terminatesExprList(p.tok) {
			break
		}
		list = append(list, p.parsePrimaryWithSuffix())
	}
	return &TupleExpr{List: list}
}

// simple_stmt = small_stmt {';' small_stmt} [';'] NEWLINE
func (p *parser) parseSimpleStmt(stmts []Stmt) []Stmt {
	for {
		stmts = append(stmts, p.parseSmallStmt())
		if p.tok != SEMI {
			break
		}
		p.nextToken()
		if p.tok == NEWLINE || p.tok == EOF {
			break
		}
	}
	// EOF without NEWLINE occurs in "if x: pass", for example.
	if p.tok != EOF {
		p.consume(NEWLINE)
	}
	return stmts
}

// small_stmt = RETURN [expr]
//
//	| PASS | BREAK | CONTINUE
//	| LOAD ...
//	| 'type' IDENT [type_params] '=' type_expr
//	| IDENT ':' type_expr ['=' expr]
//	| expr ('=' | augmented_assignment_operator) expr
//	| expr
func (p *parser) parseSmallStmt() Stmt {
	switch p.tok {
	case RETURN:
		returnPos := p.nextToken()
		var result Expr
		if p.tok != EOF && p.tok != NEWLINE && p.tok != SEMI {
			result = p.parseExpr(false)
		}
		return &ReturnStmt{Return: returnPos, Result: result}
	case BREAK, CONTINUE, PASS:
		tok := p.tok
		return &BranchStmt{Token: tok, TokenPos: p.nextToken()}
	case LOAD:
		return p.parseLoadStmt()
	case IDENT:
		// "type" is not a keyword. It only introduces a type
		// alias statement if it is followed by an identifier.
		if p.tokval.raw == "type" && p.sc.options.TypeSyntax && p.peek() == IDENT {
			return p.parseTypeAliasStmt()
		}
	}

	x := p.parseExpr(false)

	var typ Expr
	if p.tok == COLON {
		p.checkTypeSyntax("variable type annotations")
		if _, ok := x.(*Ident); !ok {
			p.errorf(p.tokval.pos, "type annotations must have a single identifier on the left-hand side")
		}
		p.nextToken()
		typ = p.parseTypeExpr()
		if p.tok != EQ {
			if isAugmentedAssignmentOperator(p.tok) {
				p.errorf(p.tokval.pos, "type annotations are not permitted on augmented assignments")
			}
			return &VarStmt{Name: x.(*Ident), Type: typ}
		}
	}

	if p.tok == EQ || isAugmentedAssignmentOperator(p.tok) {
		op := p.tok
		opPos := p.nextToken()
		return &AssignStmt{
			LHS:   x,
			Type:  typ,
			OpPos: opPos,
			Op:    op,
			RHS:   p.parseExpr(false),
		}
	}
	return &ExprStmt{X: x}
}

func isAugmentedAssignmentOperator(t Token) bool {
	return t >= PLUSEQ && t <= GTGTEQ
}

// type_alias_stmt = 'type' IDENT [type_params] '=' type_expr
func (p *parser) parseTypeAliasStmt() Stmt {
	typePos := p.nextToken()
	name := p.parseIdent()
	var typeParams []*Ident
	if p.tok == LBRACK {
		typeParams = p.parseTypeParams()
	}
	eq := p.consume(EQ)
	return &TypeAliasStmt{
		TypePos:    typePos,
		Name:       name,
		TypeParams: typeParams,
		Eq:         eq,
		Value:      p.parseTypeExpr(),
	}
}

// load_stmt = LOAD '(' STRING {',' (IDENT '=')? STRING} [','] ')'
func (p *parser) parseLoadStmt() *LoadStmt {
	loadPos := p.nextToken()
	lparen := p.consume(LPAREN)
	if p.tok != STRING {
		p.errorf(p.tokval.pos, "first operand of load statement must be a string literal")
	}
	module := p.parseLiteral()

	var bindings []*LoadBinding
	for p.tok != RPAREN {
		p.consume(COMMA)
		if p.tok == RPAREN {
			// Trailing comma.
			break
		}
		switch p.tok {
		case STRING:
			// load(module, "name")
			name := p.parseLiteral()
			prefixLength := 1
			if name.Raw[0] == 'r' {
				prefixLength = 2
			}
			bindings = append(bindings, &LoadBinding{
				Local: &Ident{
					NamePos: name.TokenPos.add(name.Raw[:prefixLength]),
					Name:    name.Value.(string),
				},
				Name: name,
			})
		case IDENT:
			// load(module, local = "name")
			local := p.parseIdent()
			if p.tok != EQ {
				p.errorf(p.tokval.pos, "load operand must be %#[1]v or %[1]s = \"originalname\" (want '=' after %[1]s)", local.Name)
			}
			p.nextToken()
			if p.tok != STRING {
				p.errorf(p.tokval.pos, "original name of loaded symbol must be quoted: %s = \"originalname\"", local.Name)
			}
			bindings = append(bindings, &LoadBinding{
				Local: local,
				Name:  p.parseLiteral(),
			})
		default:
			p.errorf(p.tokval.pos, "load operand must be \"name\" or localname = \"name\" (got %#v)", p.tok)
		}
	}
	rparen := p.nextToken()

	if len(bindings) == 0 {
		p.errorf(lparen, "load statement must import at least one symbol")
	}
	return &LoadStmt{
		Load:     loadPos,
		Module:   module,
		Bindings: bindings,
		Rparen:   rparen,
	}
}

// suite = simple_stmt | NEWLINE INDENT stmt {stmt} OUTDENT
func (p *parser) parseSuite() []Stmt {
	p.enterNesting()
	defer p.leaveNesting()

	if p.tok == NEWLINE {
		p.nextToken()
		p.consume(INDENT)
		var stmts []Stmt
		for p.tok != OUTDENT && p.tok != EOF {
			stmts = p.parseStmt(stmts)
		}
		p.consume(OUTDENT)
		return stmts
	}
	return p.parseSimpleStmt(nil)
}

func (p *parser) parseIdent() *Ident {
	if p.tok != IDENT {
		p.errorf(p.tokval.pos, "got %#v, want identifier", p.tok)
	}
	id := &Ident{
		NamePos: p.tokval.pos,
		Name:    p.tokval.raw,
	}
	p.nextToken()
	return id
}

// parseLiteral parses a STRING, INT or FLOAT token.
func (p *parser) parseLiteral() *Literal {
	var value any
	switch p.tok {
	case STRING:
		value = p.tokval.string
	case INT:
		if p.tokval.bigInt != nil {
			value = p.tokval.bigInt
		} else {
			value = p.tokval.int
		}
	case FLOAT:
		value = p.tokval.float
	default:
		panic("not a literal")
	}
	tok, raw := p.tok, p.tokval.raw
	return &Literal{
		Token:    tok,
		TokenPos: p.nextToken(),
		Raw:      raw,
		Value:    value,
	}
}

// params = [param {',' param} [',']]
//
// param = IDENT [':' type_expr] ['=' test]
//
//	| '*' [IDENT [':' type_expr]]
//	| '**' IDENT [':' type_expr]
//
// Type annotations are only permitted on parameters of functions, not
// lambdas. For lambdas, the parameter list is terminated by a colon.
func (p *parser) parseParams(isDef bool) []*Param {
	var params []*Param
	for p.tok != RPAREN && p.tok != COLON && p.tok != EOF {
		if len(params) > 0 {
			p.consume(COMMA)
			if p.tok == RPAREN || p.tok == COLON {
				break
			}
		}

		param := &Param{}
		switch p.tok {
		case STAR:
			param.StarPos = p.nextToken()
			if p.tok == IDENT {
				param.Kind = ParamArgs
				param.Name = p.parseIdent()
			} else {
				param.Kind = ParamStar
			}
		case STARSTAR:
			param.Kind = ParamKwargs
			param.StarPos = p.nextToken()
			param.Name = p.parseIdent()
		default:
			param.Kind = ParamNormal
			param.Name = p.parseIdent()
		}

		if isDef && param.Name != nil && p.tok == COLON {
			p.checkTypeSyntax("parameter type annotations")
			p.nextToken()
			param.Type = p.parseTypeExpr()
		}
		if param.Kind == ParamNormal && p.tok == EQ {
			param.Eq = p.nextToken()
			param.Default = p.parseTest()
		}
		params = append(params, param)
	}
	return params
}

// parseExpr parses an expression, possibly consisting of a comma
// separated list of tests, which is parsed as a tuple.
//
// In many cases parseTest needs to be called instead, to prevent
// ambiguity, such as f(x, y) vs. f((x, y)).
func (p *parser) parseExpr(inParens bool) Expr {
	x := p.parseTest()
	if p.tok != COMMA {
		return x
	}
	return &TupleExpr{List: p.parseExprs([]Expr{x}, inParens)}
}

// parseExprs parses a comma separated list of expressions, starting
// with the comma.
//
// expr_list = {',' expr} [',']
func (p *parser) parseExprs(exprs []Expr, allowTrailingComma bool) []Expr {
	for p.tok == COMMA {
		pos := p.nextToken()
		if terminatesExprList(p.tok) {
			if !allowTrailingComma {
				p.errorf(pos, "unparenthesized tuple with trailing comma")
			}
			break
		}
		exprs = append(exprs, p.parseTest())
	}
	return exprs
}

func terminatesExprList(t Token) bool {
	switch t {
	case EOF, NEWLINE, EQ, RBRACE, RBRACK, RPAREN, SEMI:
		return true
	}
	return false
}

// parseTest parses a single component expression.
//
// test = LAMBDA params ':' test
//
//	| test_prec [IF test_prec ELSE test]
func (p *parser) parseTest() Expr {
	p.enterNesting()
	defer p.leaveNesting()

	if p.tok == LAMBDA {
		return p.parseLambda(true)
	}

	x := p.parseTestPrec(0)
	if p.tok == IF {
		ifPos := p.nextToken()
		cond := p.parseTestPrec(0)
		if p.tok != ELSE {
			p.errorf(ifPos, "conditional expression without else clause")
		}
		elsePos := p.nextToken()
		return &CondExpr{
			True:    x,
			If:      ifPos,
			Cond:    cond,
			ElsePos: elsePos,
			False:   p.parseTest(),
		}
	}
	return x
}

// parseTestNoCond parses a single component expression without
// consuming a trailing "if expr else expr".
func (p *parser) parseTestNoCond() Expr {
	p.enterNesting()
	defer p.leaveNesting()

	if p.tok == LAMBDA {
		return p.parseLambda(false)
	}
	return p.parseTestPrec(0)
}

// parseLambda parses a lambda expression. If allowCond is set, the
// body may be a conditional expression.
func (p *parser) parseLambda(allowCond bool) Expr {
	lambda := p.nextToken()
	params := p.parseParams(false)
	p.consume(COLON)
	var body Expr
	if allowCond {
		body = p.parseTest()
	} else {
		body = p.parseTestNoCond()
	}
	return &LambdaExpr{
		Lambda: lambda,
		Params: params,
		Body:   body,
	}
}

func (p *parser) parseTestPrec(prec int) Expr {
	if prec >= len(precedenceLevels) {
		return p.parsePrimaryWithSuffix()
	}

	// test_prec = NOT test_prec
	if p.tok == NOT && prec == int(precedence[NOT]) {
		p.enterNesting()
		defer p.leaveNesting()

		notPos := p.nextToken()
		return &UnaryExpr{
			OpPos: notPos,
			Op:    NOT,
			X:     p.parseTestPrec(prec),
		}
	}
	return p.parseBinaryExpr(prec)
}

// parseBinaryExpr parses binary expressions using precedence
// climbing.
//
// test_prec = test_prec {OP test_prec}
func (p *parser) parseBinaryExpr(prec int) Expr {
	chain := p.beginChain()
	defer p.endChain(&chain)

	x := p.parseTestPrec(prec + 1)
	for first := true; ; first = false {
		if p.tok == NOT {
			// In this context, NOT must be followed by IN.
			// Replace NOT IN by a single NOTIN token.
			p.nextToken()
			if p.tok != IN {
				p.errorf(p.tokval.pos, "got %#v, want in", p.tok)
			}
			p.tok = NOTIN
		}

		opPrec := int(precedence[p.tok])
		if opPrec < prec {
			return x
		}

		// Comparisons are non-associative.
		if !first && opPrec == int(precedence[EQL]) {
			p.errorf(p.tokval.pos, "%s does not associate with %s (use parentheses)", x.(*BinaryExpr).Op, p.tok)
		}

		p.extendChain(&chain)
		op := p.tok
		opPos := p.nextToken()
		x = &BinaryExpr{
			X:     x,
			OpPos: opPos,
			Op:    op,
			Y:     p.parseTestPrec(opPrec + 1),
		}
	}
}

// precedenceLevels groups operators of equal precedence, from lowest
// to highest. Comparisons are non-associative, while other binary
// operators associate to the left. Unary MINUS, unary PLUS and TILDE
// have a higher precedence, and are therefore handled by
// parsePrimary().
var precedenceLevels = [...][]Token{
	{OR},
	{AND},
	{NOT},
	{EQL, NEQ, LT, GT, LE, GE, IN, NOTIN},
	{PIPE},
	{CIRCUMFLEX},
	{AMP},
	{LTLT, GTGT},
	{MINUS, PLUS},
	{STAR, PERCENT, SLASH, SLASHSLASH},
}

// precedence maps each operator to its precedence, or -1 for tokens
// that are not binary operators.
var precedence [maxToken]int8

func init() {
	for i := range precedence {
		precedence[i] = -1
	}
	for level, tokens := range precedenceLevels {
		for _, t := range tokens {
			precedence[t] = int8(level)
		}
	}
}

// primary_with_suffix = primary
//
//	| primary_with_suffix '.' IDENT
//	| primary_with_suffix slice_suffix
//	| primary_with_suffix call_suffix
func (p *parser) parsePrimaryWithSuffix() Expr {
	chain := p.beginChain()
	defer p.endChain(&chain)

	x := p.parsePrimary()
	for {
		switch p.tok {
		case DOT:
			p.extendChain(&chain)
			dot := p.nextToken()
			x = &DotExpr{X: x, Dot: dot, Name: p.parseIdent()}
		case LBRACK:
			p.extendChain(&chain)
			x = p.parseSliceSuffix(x)
		case LPAREN:
			p.extendChain(&chain)
			x = p.parseCallSuffix(x)
		default:
			return x
		}
	}
}

// slice_suffix = '[' expr ']'
//
//	| '[' [test] ':' [test] [':' [test]] ']'
func (p *parser) parseSliceSuffix(x Expr) Expr {
	lbrack := p.nextToken()
	var lo, hi, step Expr
	if p.tok != COLON {
		y := p.parseExpr(false)
		if p.tok == RBRACK {
			return &IndexExpr{X: x, Lbrack: lbrack, Y: y, Rbrack: p.nextToken()}
		}
		lo = y
	}
	if p.tok == COLON {
		p.nextToken()
		if p.tok != COLON && p.tok != RBRACK {
			hi = p.parseTest()
		}
	}
	if p.tok == COLON {
		p.nextToken()
		if p.tok != RBRACK {
			step = p.parseTest()
		}
	}
	return &SliceExpr{X: x, Lbrack: lbrack, Lo: lo, Hi: hi, Step: step, Rbrack: p.consume(RBRACK)}
}

// call_suffix = '(' [arg {',' arg} [',']] ')'
//
// arg = test | IDENT '=' test | '*' test | '**' test
func (p *parser) parseCallSuffix(fn Expr) Expr {
	lparen := p.consume(LPAREN)
	var args []*Arg
	for p.tok != RPAREN {
		if len(args) > 0 {
			p.consume(COMMA)
			if p.tok == RPAREN {
				break
			}
		}

		switch p.tok {
		case STAR, STARSTAR:
			kind := ArgArgs
			if p.tok == STARSTAR {
				kind = ArgKwargs
			}
			starPos := p.nextToken()
			args = append(args, &Arg{Kind: kind, StarPos: starPos, Value: p.parseTest()})
			continue
		}

		// Parse "test = test" and check that the first is an
		// identifier, so that the grammar remains LL(1).
		x := p.parseTest()
		if p.tok == EQ {
			name, ok := x.(*Ident)
			if !ok {
				p.errorf(p.tokval.pos, "keyword argument must have form name=expr")
			}
			eq := p.nextToken()
			args = append(args, &Arg{Kind: ArgNamed, Name: name, Eq: eq, Value: p.parseTest()})
		} else {
			args = append(args, &Arg{Kind: ArgPositional, Value: x})
		}
	}
	return &CallExpr{Fn: fn, Lparen: lparen, Args: args, Rparen: p.nextToken()}
}

// primary = IDENT
//
//	| INT | FLOAT | STRING
//	| '[' ...                    // list literal or comprehension
//	| '{' ...                    // dict literal or comprehension
//	| '(' ...                    // tuple or parenthesized expression
//	| ('-' | '+' | '~') primary_with_suffix
//	| CAST '(' type_expr ',' test [','] ')'
//	| ISINSTANCE '(' test ',' type_expr [','] ')'
func (p *parser) parsePrimary() Expr {
	switch p.tok {
	case IDENT:
		return p.parseIdent()
	case INT, FLOAT, STRING:
		return p.parseLiteral()
	case LBRACK:
		return p.parseList()
	case LBRACE:
		return p.parseDict()
	case LPAREN:
		lparen := p.nextToken()
		if p.tok == RPAREN {
			// Empty tuple.
			return &TupleExpr{Lparen: lparen, Rparen: p.nextToken()}
		}
		x := p.parseExpr(true)
		return &ParenExpr{Lparen: lparen, X: x, Rparen: p.consume(RPAREN)}
	case MINUS, PLUS, TILDE:
		p.enterNesting()
		defer p.leaveNesting()

		op := p.tok
		opPos := p.nextToken()
		return &UnaryExpr{OpPos: opPos, Op: op, X: p.parsePrimaryWithSuffix()}
	case CAST:
		castPos := p.nextToken()
		lparen := p.consume(LPAREN)
		typ := p.parseTypeExpr()
		p.consume(COMMA)
		value := p.parseTest()
		if p.tok == COMMA {
			p.nextToken()
		}
		return &CastExpr{Cast: castPos, Lparen: lparen, Type: typ, Value: value, Rparen: p.consume(RPAREN)}
	case ISINSTANCE:
		isInstancePos := p.nextToken()
		lparen := p.consume(LPAREN)
		value := p.parseTest()
		p.consume(COMMA)
		typ := p.parseTypeExpr()
		if p.tok == COMMA {
			p.nextToken()
		}
		return &IsInstanceExpr{IsInstance: isInstancePos, Lparen: lparen, Value: value, Type: typ, Rparen: p.consume(RPAREN)}
	case ELLIPSIS:
		p.errorf(p.tokval.pos, "'...' is only permitted in type expressions")
	}
	p.errorf(p.tokval.pos, "got %#v, want primary expression", p.tok)
	panic("unreachable")
}

// list = '[' [test {',' test} [',']] ']'
//
//	| '[' test comprehension_suffix ']'
func (p *parser) parseList() Expr {
	lbrack := p.nextToken()
	if p.tok == RBRACK {
		return &ListExpr{Lbrack: lbrack, Rbrack: p.nextToken()}
	}
	x := p.parseTest()
	if p.tok == FOR {
		return p.parseComprehensionSuffix(lbrack, x, RBRACK)
	}
	exprs := []Expr{x}
	if p.tok == COMMA {
		exprs = p.parseExprs(exprs, true)
	}
	return &ListExpr{Lbrack: lbrack, List: exprs, Rbrack: p.consume(RBRACK)}
}

// dict = '{' [dict_entry {',' dict_entry} [',']] '}'
//
//	| '{' dict_entry comprehension_suffix '}'
func (p *parser) parseDict() Expr {
	lbrace := p.nextToken()
	if p.tok == RBRACE {
		return &DictExpr{Lbrace: lbrace, Rbrace: p.nextToken()}
	}
	entry := p.parseDictEntry()
	if p.tok == FOR {
		return p.parseComprehensionSuffix(lbrace, entry, RBRACE)
	}
	entries := []*DictEntry{entry}
	for p.tok == COMMA {
		p.nextToken()
		if p.tok == RBRACE {
			break
		}
		entries = append(entries, p.parseDictEntry())
	}
	return &DictExpr{Lbrace: lbrace, Entries: entries, Rbrace: p.consume(RBRACE)}
}

// dict_entry = test ':' test
func (p *parser) parseDictEntry() *DictEntry {
	key := p.parseTest()
	colon := p.consume(COLON)
	return &DictEntry{Key: key, Colon: colon, Value: p.parseTest()}
}

// comprehension_suffix = {FOR loop_variables IN test_prec | IF test_no_cond}
//
// The first clause must be a FOR clause.
func (p *parser) parseComprehensionSuffix(lbrack Position, body Node, endBracket Token) Expr {
	p.enterNesting()
	defer p.leaveNesting()

	var clauses []Node
	for p.tok != endBracket {
		switch p.tok {
		case FOR:
			forPos := p.nextToken()
			vars := p.parseForLoopVariables()
			in := p.consume(IN)
			// Following Python 3, the operand of IN cannot be a
			// conditional expression, a lambda, or an
			// unparenthesized tuple.
			clauses = append(clauses, &ForClause{For: forPos, Vars: vars, In: in, X: p.parseTestPrec(0)})
		case IF:
			ifPos := p.nextToken()
			clauses = append(clauses, &IfClause{If: ifPos, Cond: p.parseTestNoCond()})
		default:
			p.errorf(p.tokval.pos, "got %#v, want %#v, for, or if", p.tok, endBracket)
		}
	}
	return &Comprehension{
		Curly:   endBracket == RBRACE,
		Lbrack:  lbrack,
		Body:    body,
		Clauses: clauses,
		Rbrack:  p.nextToken(),
	}
}

// type_expr = type_atom {'|' type_atom}
func (p *parser) parseTypeExpr() Expr {
	p.enterNesting()
	defer p.leaveNesting()
	chain := p.beginChain()
	defer p.endChain(&chain)

	x := p.parseTypeAtom()
	for p.tok == PIPE {
		p.extendChain(&chain)
		opPos := p.nextToken()
		x = &BinaryExpr{X: x, OpPos: opPos, Op: PIPE, Y: p.parseTypeAtom()}
	}
	return x
}

// type_atom = type_name ['[' type_arg {',' type_arg} ']']
//
//	| '(' [type_expr {',' type_expr} [',']] ')'
//
// type_name = IDENT {'.' IDENT}
func (p *parser) parseTypeAtom() Expr {
	switch p.tok {
	case IDENT:
		chain := p.beginChain()
		defer p.endChain(&chain)

		var x Expr = p.parseIdent()
		for p.tok == DOT {
			p.extendChain(&chain)
			dot := p.nextToken()
			x = &DotExpr{X: x, Dot: dot, Name: p.parseIdent()}
		}
		if p.tok != LBRACK {
			return x
		}
		lbrack := p.nextToken()
		args := []Expr{p.parseTypeArg()}
		for p.tok == COMMA {
			p.nextToken()
			if p.tok == RBRACK {
				p.errorf(p.tokval.pos, "trailing commas are not permitted in type applications")
			}
			args = append(args, p.parseTypeArg())
		}
		return &TypeApplication{Type: x, Lbrack: lbrack, Args: args, Rbrack: p.consume(RBRACK)}
	case LPAREN:
		lparen := p.nextToken()
		if p.tok == RPAREN {
			// Empty tuple.
			return &TupleExpr{Lparen: lparen, Rparen: p.nextToken()}
		}
		var x Expr = p.parseTypeExpr()
		if p.tok == COMMA {
			list := []Expr{x}
			for p.tok == COMMA {
				p.nextToken()
				if p.tok == RPAREN {
					break
				}
				list = append(list, p.parseTypeExpr())
			}
			x = &TupleExpr{List: list}
		}
		return &ParenExpr{Lparen: lparen, X: x, Rparen: p.consume(RPAREN)}
	}
	p.errorf(p.tokval.pos, "got %#v, want type expression", p.tok)
	panic("unreachable")
}

// type_arg = type_expr
//
//	| STRING
//	| '...'
//	| '[' [type_arg {',' type_arg} [',']] ']'
//	| '{' [STRING ':' type_arg {',' STRING ':' type_arg} [',']] '}'
func (p *parser) parseTypeArg() Expr {
	p.enterNesting()
	defer p.leaveNesting()

	switch p.tok {
	case STRING:
		return p.parseLiteral()
	case ELLIPSIS:
		return &EllipsisExpr{Pos: p.nextToken()}
	case LBRACK:
		lbrack := p.nextToken()
		var list []Expr
		for p.tok != RBRACK {
			if len(list) > 0 {
				p.consume(COMMA)
				if p.tok == RBRACK {
					break
				}
			}
			list = append(list, p.parseTypeArg())
		}
		return &ListExpr{Lbrack: lbrack, List: list, Rbrack: p.nextToken()}
	case LBRACE:
		lbrace := p.nextToken()
		var entries []*DictEntry
		for p.tok != RBRACE {
			if len(entries) > 0 {
				p.consume(COMMA)
				if p.tok == RBRACE {
					break
				}
			}
			if p.tok != STRING {
				p.errorf(p.tokval.pos, "got %#v, want string literal", p.tok)
			}
			key := p.parseLiteral()
			colon := p.consume(COLON)
			entries = append(entries, &DictEntry{Key: key, Colon: colon, Value: p.parseTypeArg()})
		}
		return &DictExpr{Lbrace: lbrace, Entries: entries, Rbrace: p.nextToken()}
	}
	return p.parseTypeExpr()
}
