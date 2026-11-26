package jsonshift

import (
	"fmt"
	"math"
	"strconv"
)

// countRefs 统计表达式里的字段引用个数。
func countRefs(n exprNode) int {
	switch t := n.(type) {
	case *refNode:
		return 1
	case *unaryNode:
		return countRefs(t.x)
	case *binNode:
		return countRefs(t.l) + countRefs(t.r)
	case *condNode:
		return countRefs(t.c) + countRefs(t.t) + countRefs(t.f)
	case *callNode:
		total := 0
		for _, a := range t.args {
			total += countRefs(a)
		}
		return total
	default:
		return 0
	}
}

// invertExpr 把 y = f(x) 解成 x = f⁻¹(y)，逆表达式里用 inNode 代表 y。
// 要求 x 在 f 中恰好出现一次，且从根到 x 的路径上每一步都可逆。
func invertExpr(n exprNode, acc exprNode) (exprNode, error) {
	switch t := n.(type) {
	case *refNode:
		return acc, nil

	case *unaryNode:
		switch t.op {
		case "-":
			return invertExpr(t.x, &unaryNode{op: "-", x: acc})
		case "~":
			return invertExpr(t.x, &unaryNode{op: "~", x: acc}) // 按位取反是自逆的
		default:
			return nil, fmt.Errorf("一元运算 %q 不可逆", t.op)
		}

	case *condNode:
		return nil, fmt.Errorf("条件表达式 ?: 不可逆")

	case *callNode:
		idx := -1
		for i, a := range t.args {
			if countRefs(a) > 0 {
				if idx >= 0 {
					return nil, fmt.Errorf("函数 %s 有多个参数含字段引用，无法求逆", t.name)
				}
				idx = i
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("函数 %s 的参数都不含字段引用", t.name)
		}
		if t.fn == nil {
			return nil, fmt.Errorf("函数 %q 未在编译期解析", t.name)
		}
		if t.fn.Invert == nil {
			return nil, fmt.Errorf("函数 %s 不可逆", t.name)
		}
		inner, err := t.fn.Invert(t.args, idx, acc)
		if err != nil {
			return nil, err
		}
		return invertExpr(t.args[idx], inner)

	case *binNode:
		lHas, rHas := countRefs(t.l) > 0, countRefs(t.r) > 0
		switch {
		case lHas && rHas:
			return nil, fmt.Errorf("运算 %q 两侧都含字段引用，无法求逆", t.op)
		case lHas:
			// y = x op R  =>  x = y op⁻¹ R
			switch t.op {
			case "+":
				return invertExpr(t.l, bin("-", acc, t.r))
			case "-":
				return invertExpr(t.l, bin("+", acc, t.r))
			case "*":
				return invertExpr(t.l, bin("/", acc, t.r))
			case "/":
				return invertExpr(t.l, bin("*", acc, t.r))
			case "^":
				// y = x^n  =>  x = y^(1/n)
				return invertExpr(t.l, bin("^", acc, bin("/", num(1), t.r)))
			case "<<":
				// 左移未溢出时右移可完整还原
				return invertExpr(t.l, bin(">>", acc, t.r))
			}
		case rHas:
			switch t.op {
			case "+":
				return invertExpr(t.r, bin("-", acc, t.l))
			case "-": // y = L - x  =>  x = L - y
				return invertExpr(t.r, bin("-", t.l, acc))
			case "*":
				return invertExpr(t.r, bin("/", acc, t.l))
			case "/": // y = L / x  =>  x = L / y
				return invertExpr(t.r, bin("/", t.l, acc))
			case "^": // y = b^x  =>  x = ln(y)/ln(b)
				return invertExpr(t.r, bin("/", call1("ln", acc), call1("ln", t.l)))
			}
		}
		return nil, fmt.Errorf("运算 %q 不可逆（右移、取模、按位与或、比较与逻辑运算都会丢信息）", t.op)
	}
	return nil, fmt.Errorf("无法求逆的表达式节点 %T", n)
}

// exprNumber 渲染表达式里的字面量。常规量级用定点写法（配置者读起来顺），
// 极端量级退回科学计数法——否则 1e308 会在报错里铺开成 309 位数字。
// 两种写法都能被本引擎的词法重新读回。
func exprNumber(v float64) string {
	if a := math.Abs(v); v != 0 && (a >= 1e15 || a < 1e-6) {
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	return formatNumber(v)
}

// exprString 把表达式还原成文本，用于展示自动求出的逆表达式。
func exprString(n exprNode) string {
	switch t := n.(type) {
	case *numNode:
		return exprNumber(t.v)
	case *inNode:
		return "{.}"
	case *refNode:
		return "{" + t.raw + "}"
	case *unaryNode:
		return t.op + wrapExpr(t.x)
	case *binNode:
		return wrapExpr(t.l) + t.op + wrapExpr(t.r)
	case *condNode:
		return wrapExpr(t.c) + "?" + wrapExpr(t.t) + ":" + wrapExpr(t.f)
	case *callNode:
		s := t.name + "("
		for i, a := range t.args {
			if i > 0 {
				s += ","
			}
			s += exprString(a)
		}
		return s + ")"
	}
	return "?"
}

func wrapExpr(n exprNode) string {
	switch n.(type) {
	case *binNode, *condNode:
		return "(" + exprString(n) + ")"
	}
	return exprString(n)
}

// checkDivZero 静态检查字面量的除零与模零。自动求逆会把 *0 变成 /0，
// 所以对逆表达式做同样的检查，等于顺带拦住了乘以 0 这种不可逆的写法。
func checkDivZero(n exprNode) error {
	switch t := n.(type) {
	case *binNode:
		if t.op == "/" || t.op == "%" {
			if num, ok := t.r.(*numNode); ok && num.v == 0 {
				return fmt.Errorf("出现除以 0")
			}
		}
		if err := checkDivZero(t.l); err != nil {
			return err
		}
		return checkDivZero(t.r)
	case *unaryNode:
		return checkDivZero(t.x)
	case *condNode:
		for _, x := range []exprNode{t.c, t.t, t.f} {
			if err := checkDivZero(x); err != nil {
				return err
			}
		}
	case *callNode:
		for _, a := range t.args {
			if err := checkDivZero(a); err != nil {
				return err
			}
		}
	}
	return nil
}
