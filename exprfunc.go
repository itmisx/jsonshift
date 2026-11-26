package jsonshift

import (
	"fmt"
	"math"
	"sort"
	"sync"
)

// ExprFunc 是表达式里可调用的函数。与通用计算器的函数不同，这里多了 Invert：
// 它决定该函数能否参与自动求逆。Invert 为 nil 的函数仍然可用，但含它的表达式
// 必须显式提供 reverse，或用 only 限定成单向规则。
type ExprFunc struct {
	Name  string
	Arity int // 参数个数；-1 表示可变参数
	Fn    func(args []float64) (float64, error)

	// Invert 求解 y = f(..., x, ...) 中的第 argIdx 个参数：返回一个表达式，
	// 其中 y 用 yExpr 代入，其余参数原样可用（它们必须不含字段引用）。
	// nil 表示该函数不可逆。
	Invert func(args []exprNode, argIdx int, yExpr exprNode) (exprNode, error)

	// Bool 表示返回值是布尔语义，写入目标端时输出 true/false 而非 1/0。
	Bool bool
}

var (
	funcsMu sync.RWMutex
	funcs   = map[string]ExprFunc{}
)

// RegisterFunc 注册（或覆盖）一个表达式函数，可在 init 中调用。
func RegisterFunc(f ExprFunc) {
	if f.Name == "" || f.Fn == nil {
		panic("jsonshift: 表达式函数必须有名字和实现")
	}
	funcsMu.Lock()
	defer funcsMu.Unlock()
	funcs[f.Name] = f
}

// LookupFunc 按名字查找表达式函数。
func LookupFunc(name string) (ExprFunc, bool) {
	funcsMu.RLock()
	defer funcsMu.RUnlock()
	f, ok := funcs[name]
	return f, ok
}

// ExprFuncs 返回全部已注册的表达式函数名；带 * 的表示可自动求逆。
func ExprFuncs() []string {
	funcsMu.RLock()
	defer funcsMu.RUnlock()
	out := make([]string, 0, len(funcs))
	for n, f := range funcs {
		if f.Invert != nil {
			n += "*"
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// 便捷构造器
func num(v float64) exprNode                { return &numNode{v: v} }
func bin(op string, l, r exprNode) exprNode { return &binNode{op: op, l: l, r: r} }

// call1 在求逆时构造单参调用。此处不解析函数，由 resolveFuncs 统一补齐。
func call1(name string, x exprNode) exprNode { return &callNode{name: name, args: []exprNode{x}} }

// resolveFuncs 把表达式里所有 callNode 的函数在编译期解析好，使求值路径不再
// 访问全局注册表——既省掉每次求值的读锁，也让 Shifter 真正“编译后只读”：
// 之后再 RegisterFunc 覆盖同名函数，不会改变已编译 Shifter 的行为。
func resolveFuncs(n exprNode) error {
	switch t := n.(type) {
	case *unaryNode:
		return resolveFuncs(t.x)
	case *binNode:
		if err := resolveFuncs(t.l); err != nil {
			return err
		}
		return resolveFuncs(t.r)
	case *condNode:
		for _, x := range []exprNode{t.c, t.t, t.f} {
			if err := resolveFuncs(x); err != nil {
				return err
			}
		}
	case *callNode:
		if t.fn == nil {
			f, ok := LookupFunc(t.name)
			if !ok {
				return fmt.Errorf("未注册的函数 %q", t.name)
			}
			t.fn = &f
		}
		for _, a := range t.args {
			if err := resolveFuncs(a); err != nil {
				return err
			}
		}
	}
	return nil
}

// unary1 把单参函数的求逆统一成「换成另一个单参函数」。
func unary1(inverseName string) func([]exprNode, int, exprNode) (exprNode, error) {
	return func(_ []exprNode, _ int, y exprNode) (exprNode, error) {
		return call1(inverseName, y), nil
	}
}

func arg1(args []float64) float64 { return args[0] }

func init() {
	// —— 可自动求逆 ——
	RegisterFunc(ExprFunc{Name: "sqrt", Arity: 1,
		Fn: func(a []float64) (float64, error) {
			if a[0] < 0 {
				return 0, fmt.Errorf("sqrt 的参数不能为负")
			}
			return math.Sqrt(a[0]), nil
		},
		// x = y^2
		Invert: func(_ []exprNode, _ int, y exprNode) (exprNode, error) {
			return bin("^", y, num(2)), nil
		}})

	RegisterFunc(ExprFunc{Name: "cbrt", Arity: 1,
		Fn:     func(a []float64) (float64, error) { return math.Cbrt(a[0]), nil },
		Invert: func(_ []exprNode, _ int, y exprNode) (exprNode, error) { return bin("^", y, num(3)), nil }})

	RegisterFunc(ExprFunc{Name: "exp", Arity: 1,
		Fn:     func(a []float64) (float64, error) { return math.Exp(a[0]), nil },
		Invert: unary1("ln")})

	RegisterFunc(ExprFunc{Name: "ln", Arity: 1,
		Fn: func(a []float64) (float64, error) {
			if a[0] <= 0 {
				return 0, fmt.Errorf("ln 的参数必须为正")
			}
			return math.Log(a[0]), nil
		},
		Invert: unary1("exp")})

	RegisterFunc(ExprFunc{Name: "log10", Arity: 1,
		Fn: func(a []float64) (float64, error) {
			if a[0] <= 0 {
				return 0, fmt.Errorf("log10 的参数必须为正")
			}
			return math.Log10(a[0]), nil
		},
		Invert: func(_ []exprNode, _ int, y exprNode) (exprNode, error) { return bin("^", num(10), y), nil }})

	RegisterFunc(ExprFunc{Name: "log2", Arity: 1,
		Fn: func(a []float64) (float64, error) {
			if a[0] <= 0 {
				return 0, fmt.Errorf("log2 的参数必须为正")
			}
			return math.Log2(a[0]), nil
		},
		Invert: func(_ []exprNode, _ int, y exprNode) (exprNode, error) { return bin("^", num(2), y), nil }})

	// 三角函数只在主值区间上可逆，配置里用到时请确认取值范围。
	for _, pair := range [][2]string{{"sin", "asin"}, {"cos", "acos"}, {"tan", "atan"},
		{"asin", "sin"}, {"acos", "cos"}, {"atan", "tan"}} {
		name, inv := pair[0], pair[1]
		fn := map[string]func(float64) float64{
			"sin": math.Sin, "cos": math.Cos, "tan": math.Tan,
			"asin": math.Asin, "acos": math.Acos, "atan": math.Atan,
		}[name]
		RegisterFunc(ExprFunc{Name: name, Arity: 1,
			Fn:     func(a []float64) (float64, error) { return fn(a[0]), nil },
			Invert: unary1(inv)})
	}

	// —— 不可逆：含它们的表达式必须显式写 reverse，或 only 限定单向 ——
	oneWay := map[string]func([]float64) (float64, error){
		"abs":   func(a []float64) (float64, error) { return math.Abs(arg1(a)), nil },
		"floor": func(a []float64) (float64, error) { return math.Floor(arg1(a)), nil },
		"ceil":  func(a []float64) (float64, error) { return math.Ceil(arg1(a)), nil },
		"round": func(a []float64) (float64, error) { return math.Round(arg1(a)), nil },
		"trunc": func(a []float64) (float64, error) { return math.Trunc(arg1(a)), nil },
		"sign": func(a []float64) (float64, error) {
			switch {
			case arg1(a) > 0:
				return 1, nil
			case arg1(a) < 0:
				return -1, nil
			}
			return 0, nil
		},
	}
	for name, fn := range oneWay {
		RegisterFunc(ExprFunc{Name: name, Arity: 1, Fn: fn})
	}

	RegisterFunc(ExprFunc{Name: "min", Arity: -1, Fn: func(a []float64) (float64, error) {
		m := a[0]
		for _, v := range a[1:] {
			m = math.Min(m, v)
		}
		return m, nil
	}})
	RegisterFunc(ExprFunc{Name: "max", Arity: -1, Fn: func(a []float64) (float64, error) {
		m := a[0]
		for _, v := range a[1:] {
			m = math.Max(m, v)
		}
		return m, nil
	}})
	RegisterFunc(ExprFunc{Name: "clamp", Arity: 3, Fn: func(a []float64) (float64, error) {
		return math.Min(math.Max(a[0], a[1]), a[2]), nil
	}})
	RegisterFunc(ExprFunc{Name: "hypot", Arity: 2, Fn: func(a []float64) (float64, error) {
		return math.Hypot(a[0], a[1]), nil
	}})
	RegisterFunc(ExprFunc{Name: "atan2", Arity: 2, Fn: func(a []float64) (float64, error) {
		return math.Atan2(a[0], a[1]), nil
	}})
	RegisterFunc(ExprFunc{Name: "xor", Arity: 2, Fn: func(a []float64) (float64, error) {
		return float64(toInt(a[0]) ^ toInt(a[1])), nil
	}})

	// pow(x, c)：底数含引用时可逆
	RegisterFunc(ExprFunc{Name: "pow", Arity: 2,
		Fn: func(a []float64) (float64, error) { return math.Pow(a[0], a[1]), nil },
		Invert: func(args []exprNode, argIdx int, y exprNode) (exprNode, error) {
			if argIdx != 0 {
				return nil, fmt.Errorf("pow 只能对底数求逆")
			}
			return bin("^", y, bin("/", num(1), args[1])), nil
		}})

	// bool(x)：把数值按真假写成 true/false，逆向把 true/false 当 1/0 读回
	RegisterFunc(ExprFunc{Name: "bool", Arity: 1, Bool: true,
		Fn:     func(a []float64) (float64, error) { return boolToF(arg1(a) != 0), nil },
		Invert: func(_ []exprNode, _ int, y exprNode) (exprNode, error) { return y, nil }})
}
