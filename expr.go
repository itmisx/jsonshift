package jsonshift

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 表达式引擎。与通用计算器的区别在于：每个算子都标注了**能否自动求逆**。
// 配置里只写上行表达式，下行所需的逆表达式由引擎推导；推不出来的，在保存配置
// 时就报错，要求显式给出 reverse 或用 only 限定成单向规则——而不是等到真的
// 下发指令时才发现算不回去。

// ---------- AST ----------

type exprNode interface{ node() }

type numNode struct{ v float64 }
type refNode struct {
	raw  string
	segs []segment
	self bool // 指向本条规则自己的源值
}
type inNode struct{} // 仅出现在逆表达式里：代表正向表达式的输出
type unaryNode struct {
	op string
	x  exprNode
} // - ! ~
type binNode struct {
	op   string
	l, r exprNode
}                                        // 二元
type condNode struct{ c, t, f exprNode } // ?:
type callNode struct {
	name string
	args []exprNode
	fn   *ExprFunc // 编译期解析好的函数，求值时不再查注册表
}

func (*numNode) node()   {}
func (*refNode) node()   {}
func (*inNode) node()    {}
func (*unaryNode) node() {}
func (*binNode) node()   {}
func (*condNode) node()  {}
func (*callNode) node()  {}

// ---------- 词法 ----------

type tokKind int

const (
	tkNum tokKind = iota
	tkRef
	tkIdent
	tkOp
	tkLParen
	tkRParen
	tkComma
	tkQuestion
	tkColon
)

type token struct {
	kind tokKind
	num  float64
	text string
}

var operators = []string{"<<", ">>", "<=", ">=", "==", "!=", "&&", "||",
	"+", "-", "*", "/", "%", "^", "&", "|", "<", ">", "!", "~"}

func tokenize(src string) ([]token, error) {
	var out []token
	r := []rune(src)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			out = append(out, token{kind: tkLParen})
			i++
		case c == ')':
			out = append(out, token{kind: tkRParen})
			i++
		case c == ',':
			out = append(out, token{kind: tkComma})
			i++
		case c == '?':
			out = append(out, token{kind: tkQuestion})
			i++
		case c == ':':
			out = append(out, token{kind: tkColon})
			i++
		case c == '{':
			j := -1
			for k := i + 1; k < len(r); k++ {
				if r[k] == '}' {
					j = k
					break
				}
			}
			if j < 0 {
				return nil, fmt.Errorf("表达式 %q 里的 { 缺少配对的 }", src)
			}
			ref := strings.TrimSpace(string(r[i+1 : j]))
			if ref == "" {
				return nil, fmt.Errorf("表达式 %q 里有空引用 {}", src)
			}
			out = append(out, token{kind: tkRef, text: ref})
			i = j + 1
		case isDigit(c) || c == '.':
			n, w, err := scanNumber(r[i:])
			if err != nil {
				return nil, fmt.Errorf("表达式 %q: %w", src, err)
			}
			out = append(out, token{kind: tkNum, num: n})
			i += w
		case isIdentStart(c):
			j := i
			for j < len(r) && isIdentPart(r[j]) {
				j++
			}
			out = append(out, token{kind: tkIdent, text: string(r[i:j])})
			i = j
		default:
			matched := ""
			for _, op := range operators {
				if strings.HasPrefix(string(r[i:]), op) {
					matched = op
					break
				}
			}
			if matched == "" {
				return nil, fmt.Errorf("表达式 %q 里有无法识别的字符 %q", src, string(c))
			}
			out = append(out, token{kind: tkOp, text: matched})
			i += len([]rune(matched))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("表达式为空")
	}
	return out, nil
}

func isDigit(c rune) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c rune) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isIdentPart(c rune) bool  { return isIdentStart(c) || isDigit(c) }

// scanNumber 支持十进制、科学计数法、0x 十六进制、0b 二进制，以及 _ 分隔符。
func scanNumber(r []rune) (float64, int, error) {
	if len(r) > 2 && r[0] == '0' && (r[1] == 'x' || r[1] == 'X' || r[1] == 'b' || r[1] == 'B') {
		base, digits := 16, "0123456789abcdefABCDEF"
		if r[1] == 'b' || r[1] == 'B' {
			base, digits = 2, "01"
		}
		j := 2
		var sb strings.Builder
		for j < len(r) && (strings.ContainsRune(digits, r[j]) || r[j] == '_') {
			if r[j] != '_' {
				sb.WriteRune(r[j])
			}
			j++
		}
		if sb.Len() == 0 {
			return 0, 0, fmt.Errorf("%q 后面缺少数字", string(r[:2]))
		}
		n, err := strconv.ParseInt(sb.String(), base, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("%q 不是合法数字", string(r[:j]))
		}
		return float64(n), j, nil
	}

	j := 0
	var sb strings.Builder
	for j < len(r) && (isDigit(r[j]) || r[j] == '.' || r[j] == '_') {
		if r[j] != '_' {
			sb.WriteRune(r[j])
		}
		j++
	}
	if j < len(r) && (r[j] == 'e' || r[j] == 'E') {
		k := j + 1
		if k < len(r) && (r[k] == '+' || r[k] == '-') {
			k++
		}
		if k < len(r) && isDigit(r[k]) {
			sb.WriteRune('e')
			if r[j+1] == '+' || r[j+1] == '-' {
				sb.WriteRune(r[j+1])
			}
			for k < len(r) && isDigit(r[k]) {
				sb.WriteRune(r[k])
				k++
			}
			j = k
		}
	}
	v, err := strconv.ParseFloat(sb.String(), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("%q 不是合法数字", sb.String())
	}
	return v, j, nil
}

// ---------- 语法 ----------

// 优先级由低到高，照 Go 的表：位运算比比较绑得更紧，
// 所以 {x}&1==1 读作 ({x}&1)==1，没有 C 里那个经典的坑。
// ? : 单独处理（右结合）。
var precedence = map[string]int{
	"||": 1,
	"&&": 2,
	"==": 3, "!=": 3, "<": 3, "<=": 3, ">": 3, ">=": 3,
	"+": 4, "-": 4, "|": 4,
	"*": 5, "/": 5, "%": 5, "<<": 5, ">>": 5, "&": 5,
	"^": 7, // 幂，右结合，比一元运算符更紧
}

type parser struct {
	toks []token
	pos  int
	src  string
}

func parseExpr(src string) (exprNode, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, src: src}
	n, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("表达式 %q 第 %d 个记号之后有多余内容", src, p.pos)
	}
	return n, nil
}

func (p *parser) peek() (token, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return token{}, false
}

func (p *parser) parseTernary() (exprNode, error) {
	cond, err := p.parseBinary(1)
	if err != nil {
		return nil, err
	}
	t, ok := p.peek()
	if !ok || t.kind != tkQuestion {
		return cond, nil
	}
	p.pos++
	yes, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	if c, ok := p.peek(); !ok || c.kind != tkColon {
		return nil, fmt.Errorf("表达式 %q 里的 ? 缺少配对的 :", p.src)
	}
	p.pos++
	no, err := p.parseTernary()
	if err != nil {
		return nil, err
	}
	return &condNode{c: cond, t: yes, f: no}, nil
}

func (p *parser) parseBinary(minPrec int) (exprNode, error) {
	lhs, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t, ok := p.peek()
		if !ok || t.kind != tkOp {
			return lhs, nil
		}
		prec, isBin := precedence[t.text]
		if !isBin || prec < minPrec {
			return lhs, nil
		}
		p.pos++
		next := prec + 1
		if t.text == "^" {
			next = prec // 右结合
		}
		rhs, err := p.parseBinary(next)
		if err != nil {
			return nil, err
		}
		lhs = &binNode{op: t.text, l: lhs, r: rhs}
	}
}

func (p *parser) parseUnary() (exprNode, error) {
	t, ok := p.peek()
	if ok && t.kind == tkOp && (t.text == "-" || t.text == "+" || t.text == "!" || t.text == "~") {
		p.pos++
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if t.text == "+" {
			return x, nil
		}
		return &unaryNode{op: t.text, x: x}, nil
	}
	return p.parsePower()
}

// parsePower 让 ^ 比一元负号绑得更紧：-2^2 等于 -(2^2)。
func (p *parser) parsePower() (exprNode, error) {
	base, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	if t, ok := p.peek(); ok && t.kind == tkOp && t.text == "^" {
		p.pos++
		exp, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &binNode{op: "^", l: base, r: exp}, nil
	}
	return base, nil
}

func (p *parser) parsePrimary() (exprNode, error) {
	t, ok := p.peek()
	if !ok {
		return nil, fmt.Errorf("表达式 %q 意外结束", p.src)
	}
	switch t.kind {
	case tkNum:
		p.pos++
		return &numNode{v: t.num}, nil
	case tkRef:
		p.pos++
		segs, err := parsePath(t.text)
		if err != nil && t.text != "." {
			return nil, fmt.Errorf("引用 {%s}: %w", t.text, err)
		}
		return &refNode{raw: t.text, segs: segs}, nil
	case tkIdent:
		p.pos++
		if c, ok := constants[t.text]; ok {
			return &numNode{v: c}, nil
		}
		if n, ok := p.peek(); !ok || n.kind != tkLParen {
			return nil, fmt.Errorf("表达式 %q 里的 %q 既不是常量也不是函数调用", p.src, t.text)
		}
		p.pos++ // 吃掉 (
		var args []exprNode
		if n, ok := p.peek(); ok && n.kind == tkRParen {
			p.pos++
		} else {
			for {
				a, err := p.parseTernary()
				if err != nil {
					return nil, err
				}
				args = append(args, a)
				n, ok := p.peek()
				if !ok {
					return nil, fmt.Errorf("函数 %s( 缺少右括号", t.text)
				}
				p.pos++
				if n.kind == tkRParen {
					break
				}
				if n.kind != tkComma {
					return nil, fmt.Errorf("函数 %s 的参数之间应用逗号分隔", t.text)
				}
			}
		}
		f, ok := LookupFunc(t.text)
		if !ok {
			return nil, fmt.Errorf("未注册的函数 %q", t.text)
		}
		if f.Arity >= 0 && len(args) != f.Arity {
			return nil, fmt.Errorf("函数 %s 需要 %d 个参数，给了 %d 个", t.text, f.Arity, len(args))
		}
		if f.Arity < 0 && len(args) == 0 {
			return nil, fmt.Errorf("函数 %s 至少需要一个参数", t.text)
		}
		return &callNode{name: t.text, args: args, fn: &f}, nil
	case tkLParen:
		p.pos++
		n, err := p.parseTernary()
		if err != nil {
			return nil, err
		}
		if c, ok := p.peek(); !ok || c.kind != tkRParen {
			return nil, fmt.Errorf("表达式 %q 缺少右括号", p.src)
		}
		p.pos++
		return n, nil
	default:
		return nil, fmt.Errorf("表达式 %q 里有多余的记号", p.src)
	}
}

var constants = map[string]float64{
	"pi": math.Pi,
	"e":  math.E,
}

// ---------- 求值 ----------

type evalCtx struct {
	self any // 本条规则当前的源值；逆表达式里的 inNode 读的也是它
	src  any // 整份源文档，供跨字段引用使用
}

func evalExpr(n exprNode, ctx *evalCtx) (float64, error) {
	switch t := n.(type) {
	case *numNode:
		return t.v, nil
	case *inNode:
		// 逆表达式的输入就是当前读到的源值
		return exprFloat(ctx.self)
	case *refNode:
		if t.self {
			return exprFloat(ctx.self)
		}
		v, ok := getPath(ctx.src, t.segs)
		if !ok {
			return 0, fmt.Errorf("引用 {%s} 在源数据里不存在", t.raw)
		}
		return exprFloat(v)

	case *unaryNode:
		x, err := evalExpr(t.x, ctx)
		if err != nil {
			return 0, err
		}
		switch t.op {
		case "-":
			return -x, nil
		case "!":
			return boolToF(x == 0), nil
		case "~":
			return float64(^toInt(x)), nil
		}

	case *condNode:
		c, err := evalExpr(t.c, ctx)
		if err != nil {
			return 0, err
		}
		if c != 0 {
			return evalExpr(t.t, ctx)
		}
		return evalExpr(t.f, ctx)

	case *callNode:
		if t.fn == nil {
			return 0, fmt.Errorf("函数 %q 未在编译期解析", t.name)
		}
		args := make([]float64, len(t.args))
		for i, a := range t.args {
			v, err := evalExpr(a, ctx)
			if err != nil {
				return 0, err
			}
			args[i] = v
		}
		return t.fn.Fn(args)

	case *binNode:
		// && 与 || 短路
		if t.op == "&&" || t.op == "||" {
			l, err := evalExpr(t.l, ctx)
			if err != nil {
				return 0, err
			}
			if t.op == "&&" && l == 0 {
				return 0, nil
			}
			if t.op == "||" && l != 0 {
				return 1, nil
			}
			r, err := evalExpr(t.r, ctx)
			if err != nil {
				return 0, err
			}
			return boolToF(r != 0), nil
		}
		l, err := evalExpr(t.l, ctx)
		if err != nil {
			return 0, err
		}
		r, err := evalExpr(t.r, ctx)
		if err != nil {
			return 0, err
		}
		switch t.op {
		case "+":
			return l + r, nil
		case "-":
			return l - r, nil
		case "*":
			return l * r, nil
		case "/":
			if r == 0 {
				return 0, fmt.Errorf("表达式出现除以 0")
			}
			return l / r, nil
		case "%":
			if int64(r) == 0 {
				return 0, fmt.Errorf("表达式出现对 0 取模")
			}
			return float64(toInt(l) % toInt(r)), nil
		case "^":
			return math.Pow(l, r), nil
		case "&":
			return float64(toInt(l) & toInt(r)), nil
		case "|":
			return float64(toInt(l) | toInt(r)), nil
		case "<<":
			return float64(toInt(l) << uint(toInt(r))), nil
		case ">>":
			return float64(toInt(l) >> uint(toInt(r))), nil
		case "==":
			return boolToF(l == r), nil
		case "!=":
			return boolToF(l != r), nil
		case "<":
			return boolToF(l < r), nil
		case "<=":
			return boolToF(l <= r), nil
		case ">":
			return boolToF(l > r), nil
		case ">=":
			return boolToF(l >= r), nil
		}
	}
	return 0, fmt.Errorf("表达式节点类型异常 %T", n)
}

func boolToF(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func toInt(f float64) int64 { return int64(f) }

// exprFloat 把源值转成数字；布尔按 1/0 处理，数字字符串也接受，
// 因为设备把数值塞在字符串里上报很常见。
func exprFloat(v any) (float64, error) {
	switch t := v.(type) {
	case bool:
		return boolToF(t), nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, fmt.Errorf("%q 不是数字", t)
		}
		return f, nil
	default:
		return toFloat(v)
	}
}

// isBoolResult 判断表达式的结果是否是布尔语义，用于决定写出 true/false 还是数字。
func isBoolResult(n exprNode) bool {
	switch t := n.(type) {
	case *unaryNode:
		return t.op == "!"
	case *binNode:
		switch t.op {
		case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
			return true
		}
	case *condNode:
		return isBoolResult(t.t) && isBoolResult(t.f)
	case *callNode:
		if t.fn != nil {
			return t.fn.Bool
		}
	}
	return false
}
