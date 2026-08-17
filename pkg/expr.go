package pkg

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Value is a runtime value: either a string or a list of strings.
type Value struct {
	IsList bool
	S      string
	L      []string
}

func StringValue(s string) Value { return Value{S: s} }

func ListValue(items []string) Value { return Value{IsList: true, L: items} }

func (v Value) String() string {
	if v.IsList {
		return strings.Join(v.L, ", ")
	}
	return v.S
}

func (v Value) Joined() string {
	if v.IsList {
		return strings.Join(v.L, " ")
	}
	return v.S
}

func (v Value) Items() []string {
	if v.IsList {
		return v.L
	}
	if v.S == "" {
		return nil
	}
	return []string{v.S}
}

func toBool(v Value) bool {
	if v.IsList {
		return len(v.L) > 0
	}
	switch strings.TrimSpace(v.S) {
	case "", "false", "0":
		return false
	}
	return true
}

func boolValue(b bool) Value {
	if b {
		return StringValue("true")
	}
	return StringValue("false")
}

func isIntStr(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '-' {
		i = 1
	}
	digits := 0
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
		digits++
	}
	return digits > 0
}

type EvalContext interface {
	LookupVar(name string) (Value, bool)
	LookupEnv(name string) (string, bool)
	LookupState(name string) (string, bool)
	BaseDir() string
}

func LookupVariableIndexed(data *ParsedData, name, scope string) (Value, bool) {
	if v, ok := data.LookupVariableValue(name, scope); ok {
		return v, true
	}
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		if idx, err := strconv.Atoi(name[dot+1:]); err == nil {
			if v, ok := data.LookupVariableValue(name[:dot], scope); ok && v.IsList {
				if idx >= 0 && idx < len(v.L) {
					return StringValue(v.L[idx]), true
				}
				return StringValue(""), true
			}
		}
	}
	return Value{}, false
}

func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(s[i+1])
			i++
		case c == '"':
			inQ = !inQ
		case (c == ' ' || c == '\t') && !inQ:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// resolveStateRefsWith replaces @state("name") / state("name") with the value.
func resolveStateRefsWith(s string, lookup func(string) (string, bool)) string {
	if !strings.Contains(s, "state") {
		return s
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i])
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		rest := s[i:]
		var after int
		switch {
		case strings.HasPrefix(rest, "@state("):
			after = i + len("@state(")
		case strings.HasPrefix(rest, "state("):
			after = i + len("state(")
		default:
			b.WriteByte(s[i])
			i++
			continue
		}
		j := after
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j < len(s) && s[j] == '"' {
			j++
			argStart := j
			for j < len(s) && s[j] != '"' {
				j++
			}
			if j < len(s) {
				if val, ok := lookup(s[argStart:j]); ok {
					b.WriteString(val)
					j++
					for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
						j++
					}
					if j < len(s) && s[j] == ')' {
						j++
					}
					i = j
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

type exprTokKind int

const (
	tokEOF exprTokKind = iota
	tokNum
	tokStr
	tokRef
	tokEnv
	tokState
	tokList
	tokFunc
	tokWord
	tokOp
	tokLParen
	tokRParen
)

type exprTok struct {
	kind   exprTokKind
	text   string
	val    Value
	raw    string
	def    string
	hasDef bool
}

func isExprOpByte(c byte) bool {
	switch c {
	case '+', '-', '*', '/', '%', '?', ':', '=', '>', '<', '!', ',', '|':
		return true
	}
	return false
}

func isExprBreak(c byte) bool {
	if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
		return true
	}
	return c == '&' || c == '@' || c == '"' || c == '(' || c == ')' || c == '[' || c == ']' || isExprOpByte(c)
}

func scanBalanced(s string, i int, open, close byte) (int, string, error) {
	depth := 0
	inQ := false
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '"':
			inQ = !inQ
		case open:
			if !inQ {
				depth++
			}
		case close:
			if !inQ {
				depth--
				if depth == 0 {
					return j + 1, s[i+1 : j], nil
				}
			}
		}
	}
	return 0, "", fmt.Errorf("unbalanced %q", string(open))
}

// splitTopLevel splits s on sep at paren/bracket depth zero, outside quotes.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth := 0
	inQ := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQ = !inQ
		case '(', '[':
			if !inQ {
				depth++
			}
		case ')', ']':
			if !inQ {
				depth--
			}
		default:
			if s[i] == sep && depth == 0 && !inQ {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out
}

func substituteInner(s string, ctx EvalContext) string {
	if !strings.ContainsAny(s, "&@") {
		return s
	}
	s = resolveStateRefsWith(s, func(n string) (string, bool) { return ctx.LookupState(n) })
	s = scanRefs(s, '&', isVarIdentRune, isPlainRune, func(name string) (string, bool) {
		v, ok := ctx.LookupVar(name)
		if !ok {
			return "", true
		}
		if v.IsList {
			return v.String(), true
		}
		return v.S, true
	}, true)
	return scanRefs(s, '@', isPlainRune, nil, func(token string) (string, bool) {
		name, def, hasDef := splitEnvRefToken(token)
		if val, ok := ctx.LookupEnv(name); ok {
			return val, true
		}
		if hasDef {
			return def, true
		}
		return "", true
	}, false)
}

func tokenizeExpr(s string, ctx EvalContext) ([]exprTok, error) {
	var toks []exprTok
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '&':
			if i+1 < n && s[i+1] == '&' {
				toks = append(toks, exprTok{kind: tokOp, text: "&&"})
				i += 2
				continue
			}
			j := i + 1
			start := j
			for j < n && isVarIdentByte(s[j]) {
				j++
			}
			for j < n && s[j] == '.' && j+1 < n && (isPlainRune(rune(s[j+1])) || s[j+1] == '*') {
				j += 2
				for j < n && isPlainRune(rune(s[j])) {
					j++
				}
			}
			if j == start {
				return nil, fmt.Errorf("bare '&'")
			}
			toks = append(toks, exprTok{kind: tokRef, text: s[start:j]})
			i = j
		case c == '@':
			j := i + 1
			start := j
			for j < n && isPlainRune(rune(s[j])) {
				j++
			}
			if j == start {
				return nil, fmt.Errorf("bare '@'")
			}
			name := s[start:j]
			if name == "state" && j < n && s[j] == '(' {
				if end, arg, ok := tryStateCall(s, j); ok {
					toks = append(toks, exprTok{kind: tokState, text: arg})
					i = end
					break
				}
			}
			def := ""
			hasDef := false
			if j+1 < n && s[j] == ':' && s[j+1] == '-' {
				j += 2
				ds := j
				for j < n && !isEnvDefaultEnd(rune(s[j])) {
					j++
				}
				def = s[ds:j]
				hasDef = true
			}
			toks = append(toks, exprTok{kind: tokEnv, text: name, def: def, hasDef: hasDef})
			i = j
		case c == '"':
			j := i + 1
			var b strings.Builder
			for j < n && s[j] != '"' {
				if s[j] == '\\' && j+1 < n && (s[j+1] == '\\' || s[j+1] == '"') {
					b.WriteByte(s[j+1])
					j += 2
					continue
				}
				b.WriteByte(s[j])
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("unterminated string")
			}
			toks = append(toks, exprTok{kind: tokStr, val: StringValue(substituteInner(b.String(), ctx))})
			i = j + 1
		case c == '[':
			j, raw, err := scanBalanced(s, i, '[', ']')
			if err != nil {
				return nil, err
			}
			toks = append(toks, exprTok{kind: tokList, raw: raw})
			i = j
		case c == '(':
			toks = append(toks, exprTok{kind: tokLParen})
			i++
		case c == ')':
			toks = append(toks, exprTok{kind: tokRParen})
			i++
		case c >= '0' && c <= '9':
			j := i
			for j < n && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			toks = append(toks, exprTok{kind: tokNum, val: StringValue(s[i:j])})
			i = j
		case isExprOpByte(c):
			if i+1 < n {
				switch s[i : i+2] {
				case "==", "!=", "<=", ">=", "&&", "||":
					toks = append(toks, exprTok{kind: tokOp, text: s[i : i+2]})
					i += 2
					continue
				}
			}
			toks = append(toks, exprTok{kind: tokOp, text: string(c)})
			i++
		default:
			j := i
			for j < n && !isExprBreak(s[j]) {
				j++
			}
			word := s[i:j]
			k := j
			for k < n && (s[k] == ' ' || s[k] == '\t') {
				k++
			}
			if k < n && s[k] == '(' && isBuiltinFunc(word) {
				end, raw, err := scanBalanced(s, k, '(', ')')
				if err != nil {
					return nil, err
				}
				toks = append(toks, exprTok{kind: tokFunc, text: word, raw: raw})
				i = end
				continue
			}
			toks = append(toks, exprTok{kind: tokWord, text: word})
			i = j
		}
	}
	toks = append(toks, exprTok{kind: tokEOF})
	return toks, nil
}

// tryStateCall parses state("name") starting at the '(' in s[j:].
func tryStateCall(s string, j int) (end int, arg string, ok bool) {
	k := j + 1
	for k < len(s) && (s[k] == ' ' || s[k] == '\t') {
		k++
	}
	if k >= len(s) || s[k] != '"' {
		return 0, "", false
	}
	k++
	argStart := k
	for k < len(s) && s[k] != '"' {
		k++
	}
	if k >= len(s) {
		return 0, "", false
	}
	end = k + 1
	for end < len(s) && (s[end] == ' ' || s[end] == '\t') {
		end++
	}
	if end < len(s) && s[end] == ')' {
		end++
	}
	return end, s[argStart:k], true
}

type exprParser struct {
	toks []exprTok
	pos  int
	ctx  EvalContext
}

func (p *exprParser) peek() exprTok { return p.toks[p.pos] }

func (p *exprParser) next() exprTok {
	t := p.toks[p.pos]
	p.pos++
	return t
}

func (p *exprParser) parseExpr() (Value, error) { return p.parseTernary() }

func (p *exprParser) parseTernary() (Value, error) {
	cond, err := p.parseOr()
	if err != nil {
		return Value{}, err
	}
	if t := p.peek(); t.kind == tokOp && t.text == "?" {
		p.next()
		a, err := p.parseExpr()
		if err != nil {
			return Value{}, err
		}
		if t := p.peek(); t.kind != tokOp || t.text != ":" {
			return Value{}, fmt.Errorf("expected ':' in ternary expression")
		}
		p.next()
		b, err := p.parseExpr()
		if err != nil {
			return Value{}, err
		}
		if toBool(cond) {
			return a, nil
		}
		return b, nil
	}
	return cond, nil
}

func (p *exprParser) parseBinops(next func() (Value, error), ops ...string) (Value, error) {
	l, err := next()
	if err != nil {
		return Value{}, err
	}
	for {
		t := p.peek()
		if t.kind != tokOp || !slices.Contains(ops, t.text) {
			return l, nil
		}
		p.next()
		r, err := next()
		if err != nil {
			return Value{}, err
		}
		l, err = applyBinop(l, r, t.text)
		if err != nil {
			return Value{}, err
		}
	}
}

func (p *exprParser) parseOr() (Value, error)  { return p.parseBinops(p.parseAnd, "||") }
func (p *exprParser) parseAnd() (Value, error) { return p.parseBinops(p.parseEquality, "&&") }
func (p *exprParser) parseEquality() (Value, error) {
	return p.parseBinops(p.parseRelational, "==", "!=")
}
func (p *exprParser) parseRelational() (Value, error) {
	return p.parseBinops(p.parseAdditive, ">", ">=", "<", "<=")
}
func (p *exprParser) parseAdditive() (Value, error) {
	return p.parseBinops(p.parseMultiplicative, "+", "-")
}
func (p *exprParser) parseMultiplicative() (Value, error) {
	return p.parseBinops(p.parseUnary, "*", "/", "%")
}

func (p *exprParser) parseUnary() (Value, error) {
	if t := p.peek(); t.kind == tokOp && (t.text == "-" || t.text == "!") {
		p.next()
		v, err := p.parseUnary()
		if err != nil {
			return Value{}, err
		}
		if t.text == "!" {
			return boolValue(!toBool(v)), nil
		}
		if !isIntStr(v.S) {
			return Value{}, fmt.Errorf("cannot negate non-numeric value %q", v.S)
		}
		n, _ := strconv.Atoi(v.S)
		return StringValue(strconv.Itoa(-n)), nil
	}
	return p.parsePrimary()
}

func (p *exprParser) parsePrimary() (Value, error) {
	t := p.peek()
	switch t.kind {
	case tokNum, tokStr:
		p.next()
		return t.val, nil
	case tokRef:
		p.next()
		if v, ok := p.ctx.LookupVar(t.text); ok {
			return v, nil
		}
		if dot := strings.LastIndexByte(t.text, '.'); dot > 0 {
			if idx, err := strconv.Atoi(t.text[dot+1:]); err == nil {
				if v, ok := p.ctx.LookupVar(t.text[:dot]); ok && v.IsList {
					if idx >= 0 && idx < len(v.L) {
						return StringValue(v.L[idx]), nil
					}
					return StringValue(""), nil
				}
			}
		}
		return StringValue(""), nil
	case tokEnv:
		p.next()
		if val, ok := p.ctx.LookupEnv(t.text); ok {
			return StringValue(val), nil
		}
		if t.hasDef {
			return StringValue(t.def), nil
		}
		return StringValue(""), nil
	case tokState:
		p.next()
		if val, ok := p.ctx.LookupState(t.text); ok {
			return StringValue(val), nil
		}
		return StringValue(""), nil
	case tokList:
		p.next()
		return p.evalList(t.raw)
	case tokFunc:
		p.next()
		return p.evalFunc(t.text, t.raw)
	case tokLParen:
		p.next()
		v, err := p.parseExpr()
		if err != nil {
			return Value{}, err
		}
		if p.peek().kind != tokRParen {
			return Value{}, fmt.Errorf("expected ')'")
		}
		p.next()
		return v, nil
	}
	return Value{}, fmt.Errorf("bare word %q", t.text)
}

func (p *exprParser) evalList(raw string) (Value, error) {
	var out []string
	for _, item := range splitTopLevel(raw, ',') {
		if item == "" {
			continue
		}
		out = append(out, evalValueExprLoose(item, p.ctx).Items()...)
	}
	return ListValue(out), nil
}

func (p *exprParser) evalFunc(name, raw string) (Value, error) {
	var args []Value
	for _, a := range splitTopLevel(raw, ',') {
		if strings.TrimSpace(a) == "" {
			continue
		}
		args = append(args, evalValueExprLoose(a, p.ctx))
	}
	return callBuiltin(name, args, p.ctx)
}

func applyBinop(l, r Value, op string) (Value, error) {
	switch op {
	case "+":
		if l.IsList || r.IsList {
			return ListValue(append(slices.Clone(l.Items()), r.Items()...)), nil
		}
		if isIntStr(l.S) && isIntStr(r.S) {
			a, _ := strconv.Atoi(l.S)
			b, _ := strconv.Atoi(r.S)
			return StringValue(strconv.Itoa(a + b)), nil
		}
		return StringValue(l.S + r.S), nil
	case "-", "*", "/", "%":
		if !isIntStr(l.S) || !isIntStr(r.S) {
			return Value{}, fmt.Errorf("cannot apply '%s' to non-numeric operands (%q, %q)", op, l.S, r.S)
		}
		a, _ := strconv.Atoi(l.S)
		b, _ := strconv.Atoi(r.S)
		switch op {
		case "-":
			return StringValue(strconv.Itoa(a - b)), nil
		case "*":
			return StringValue(strconv.Itoa(a * b)), nil
		case "/":
			if b == 0 {
				return Value{}, fmt.Errorf("division by zero")
			}
			return StringValue(strconv.Itoa(a / b)), nil
		default:
			if b == 0 {
				return Value{}, fmt.Errorf("division by zero")
			}
			return StringValue(strconv.Itoa(a % b)), nil
		}
	case "&&":
		return boolValue(toBool(l) && toBool(r)), nil
	case "||":
		return boolValue(toBool(l) || toBool(r)), nil
	}
	return boolValue(compareValues(l.S, r.S, op)), nil
}

type builtinDef struct {
	arity func(n int) bool // nil means unconstrained
	fn    func(args []Value, ctx EvalContext) (Value, error)
}

func arity(min, max int) func(int) bool {
	return func(n int) bool { return n >= min && (max < 0 || n <= max) }
}

func unaryStr(f func(string) string) builtinDef {
	return builtinDef{arity: arity(1, 1), fn: func(args []Value, _ EvalContext) (Value, error) {
		return StringValue(f(argStr(args[0]))), nil
	}}
}

func unaryInt(f func(int) int) builtinDef {
	return builtinDef{arity: arity(1, 1), fn: func(args []Value, _ EvalContext) (Value, error) {
		n, err := strconv.Atoi(argStr(args[0]))
		if err != nil {
			return Value{}, fmt.Errorf("expected a number, got %q", argStr(args[0]))
		}
		return StringValue(strconv.Itoa(f(n))), nil
	}}
}

var lengthDef = builtinDef{arity: arity(1, 1), fn: func(args []Value, _ EvalContext) (Value, error) {
	if args[0].IsList {
		return StringValue(strconv.Itoa(len(args[0].L))), nil
	}
	return StringValue(strconv.Itoa(utf8.RuneCountInString(args[0].S))), nil
}}

var builtins = map[string]builtinDef{
	"basename": unaryStr(func(s string) string { return filepath.Base(s) }),
	"dirname":  unaryStr(func(s string) string { return filepath.ToSlash(filepath.Dir(s)) }),
	"ext":      unaryStr(func(s string) string { return filepath.Ext(s) }),
	"stem": unaryStr(func(s string) string {
		base := filepath.Base(s)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}),
	"upper":  unaryStr(strings.ToUpper),
	"lower":  unaryStr(strings.ToLower),
	"trim":   unaryStr(strings.TrimSpace),
	"abs":    unaryInt(absInt),
	"length": lengthDef,
	"len":    lengthDef,
	"exists": {arity: arity(1, 1), fn: func(args []Value, ctx EvalContext) (Value, error) {
		_, err := os.Stat(resolveBase(args[0], ctx))
		return boolValue(err == nil), nil
	}},
	"missing": {arity: arity(1, 1), fn: func(args []Value, ctx EvalContext) (Value, error) {
		_, err := os.Stat(resolveBase(args[0], ctx))
		return boolValue(err != nil), nil
	}},
	"glob": {arity: arity(1, 1), fn: func(args []Value, ctx EvalContext) (Value, error) {
		matches, _ := filepath.Glob(resolveBase(args[0], ctx))
		var items []string
		for _, m := range matches {
			items = append(items, filepath.Base(m))
		}
		return ListValue(items), nil
	}},
	"require": {arity: arity(1, 1), fn: func(args []Value, _ EvalContext) (Value, error) {
		_, err := exec.LookPath(argStr(args[0]))
		return boolValue(err == nil), nil
	}},
	"file": {arity: arity(1, 1), fn: func(args []Value, ctx EvalContext) (Value, error) {
		data, err := os.ReadFile(resolveBase(args[0], ctx))
		if err != nil {
			return StringValue(""), nil
		}
		return StringValue(strings.TrimSuffix(string(data), "\n")), nil
	}},
	"lines": {arity: arity(1, 1), fn: func(args []Value, ctx EvalContext) (Value, error) {
		data, err := os.ReadFile(resolveBase(args[0], ctx))
		if err != nil {
			return ListValue(nil), nil
		}
		var items []string
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				items = append(items, line)
			}
		}
		return ListValue(items), nil
	}},
	"sha256": {arity: arity(1, 1), fn: func(args []Value, ctx EvalContext) (Value, error) {
		return StringValue(hashFile(resolveBase(args[0], ctx))), nil
	}},
	"replace": {arity: arity(3, 3), fn: func(args []Value, _ EvalContext) (Value, error) {
		return StringValue(strings.ReplaceAll(argStr(args[0]), argStr(args[1]), argStr(args[2]))), nil
	}},
	"sprintf": {arity: arity(1, -1), fn: func(args []Value, _ EvalContext) (Value, error) {
		vals := make([]any, len(args)-1)
		for i, a := range args[1:] {
			if !a.IsList && isIntStr(a.S) {
				n, _ := strconv.Atoi(a.S)
				vals[i] = n
			} else {
				vals[i] = argStr(a)
			}
		}
		return StringValue(fmt.Sprintf(argStr(args[0]), vals...)), nil
	}},
	"min": {arity: arity(1, -1), fn: func(args []Value, _ EvalContext) (Value, error) {
		return minMax(args, true)
	}},
	"max": {arity: arity(1, -1), fn: func(args []Value, _ EvalContext) (Value, error) {
		return minMax(args, false)
	}},
	"date": {arity: arity(0, 1), fn: func(args []Value, _ EvalContext) (Value, error) {
		f := "2006-01-02"
		if len(args) == 1 {
			f = argStr(args[0])
		}
		return StringValue(time.Now().Format(f)), nil
	}},
	"uuid": {arity: arity(0, 0), fn: func(_ []Value, _ EvalContext) (Value, error) {
		return StringValue(newUUID()), nil
	}},
	"sort": {arity: arity(1, 1), fn: func(args []Value, _ EvalContext) (Value, error) {
		items := slices.Clone(args[0].Items())
		sort.Strings(items)
		return ListValue(items), nil
	}},
	"uniq": {arity: arity(1, 1), fn: func(args []Value, _ EvalContext) (Value, error) {
		var out []string
		seen := make(map[string]bool)
		for _, it := range args[0].Items() {
			if !seen[it] {
				seen[it] = true
				out = append(out, it)
			}
		}
		return ListValue(out), nil
	}},
	"join": {arity: arity(2, 2), fn: func(args []Value, _ EvalContext) (Value, error) {
		return StringValue(strings.Join(args[0].Items(), argStr(args[1]))), nil
	}},
	"split": {arity: arity(2, 2), fn: func(args []Value, _ EvalContext) (Value, error) {
		var items []string
		for part := range strings.SplitSeq(argStr(args[0]), argStr(args[1])) {
			items = append(items, part)
		}
		return ListValue(items), nil
	}},
	"env": {arity: arity(1, 1), fn: func(args []Value, ctx EvalContext) (Value, error) {
		if v, ok := ctx.LookupEnv(argStr(args[0])); ok {
			return StringValue(v), nil
		}
		return StringValue(""), nil
	}},
	"state": {arity: arity(1, 1), fn: func(args []Value, ctx EvalContext) (Value, error) {
		if v, ok := ctx.LookupState(argStr(args[0])); ok {
			return StringValue(v), nil
		}
		return StringValue(""), nil
	}},
}

func isBuiltinFunc(name string) bool { _, ok := builtins[name]; return ok }

func argStr(v Value) string {
	if v.IsList {
		return v.String()
	}
	return v.S
}

func resolveBase(v Value, ctx EvalContext) string {
	p := argStr(v)
	if ctx == nil || p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(ctx.BaseDir(), p)
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func minMax(args []Value, min bool) (Value, error) {
	best, err := strconv.Atoi(argStr(args[0]))
	if err != nil {
		return Value{}, fmt.Errorf("expected numbers")
	}
	for _, a := range args[1:] {
		n, err := strconv.Atoi(argStr(a))
		if err != nil {
			return Value{}, fmt.Errorf("expected numbers")
		}
		if (min && n < best) || (!min && n > best) {
			best = n
		}
	}
	return StringValue(strconv.Itoa(best)), nil
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst, b[:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:], b[10:])
	return string(dst)
}

func callBuiltin(name string, args []Value, ctx EvalContext) (Value, error) {
	def, ok := builtins[name]
	if !ok {
		return Value{}, fmt.Errorf("unknown function %q", name)
	}
	if def.arity != nil && !def.arity(len(args)) {
		return Value{}, fmt.Errorf("%s called with %d argument(s)", name, len(args))
	}
	return def.fn(args, ctx)
}

func evalValueExprLoose(s string, ctx EvalContext) Value {
	if v, ok, err := evalValueExpr(s, ctx); err == nil && ok {
		return v
	}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "&") {
		if v, ok := ctx.LookupVar(s[1:]); ok {
			return v
		}
	}
	return StringValue(strings.Trim(substituteInner(s, ctx), `"`))
}

// exprGate reports whether s plausibly contains an expression at top level.
func exprGate(s string) bool {
	inQ := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQ = !inQ
		case '\\':
			if i+1 < len(s) {
				i++
			}
		case '[', '(', ')':
			if !inQ {
				return true
			}
		default:
			if !inQ && isExprOpByte(s[i]) {
				return true
			}
		}
	}
	return false
}

func evalValueExpr(s string, ctx EvalContext) (Value, bool, error) {
	if !exprGate(s) {
		return Value{}, false, nil
	}
	toks, err := tokenizeExpr(s, ctx)
	if err != nil {
		return Value{}, false, nil
	}
	p := &exprParser{toks: toks, ctx: ctx}
	v, err := p.parseExpr()
	if err != nil {
		return Value{}, false, nil
	}
	if p.peek().kind != tokEOF {
		return Value{}, false, nil
	}
	for _, t := range toks {
		switch t.kind {
		case tokOp, tokList, tokFunc, tokState:
			return v, true, nil
		}
	}
	return Value{}, false, nil
}
