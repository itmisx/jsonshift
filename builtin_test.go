package jsonshift

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// 本文件覆盖 README 里列出的全部内置表达式函数与转换器，以及注册表的查询 API。
// 文档承诺了一张表，这张表就该逐项有测试兜着。

// ---------- 表达式函数：求值 ----------

func TestBuiltinFuncEval(t *testing.T) {
	cases := []struct {
		expr string
		in   float64
		want float64
	}{
		{`sqrt({v})`, 16, 4},
		{`cbrt({v})`, 27, 3},
		{`exp({v})`, 0, 1},
		{`ln({v})`, 1, 0},
		{`log10({v})`, 1000, 3},
		{`log2({v})`, 8, 3},
		{`sin({v})`, 0, 0},
		{`cos({v})`, 0, 1},
		{`tan({v})`, 0, 0},
		{`asin({v})`, 0, 0},
		{`acos({v})`, 1, 0},
		{`atan({v})`, 0, 0},
		{`abs({v})`, -7, 7},
		{`floor({v})`, 2.7, 2},
		{`ceil({v})`, 2.1, 3},
		{`round({v})`, 2.5, 3},
		{`trunc({v})`, -2.7, -2},
		{`sign({v})`, -5, -1},
		{`sign({v})`, 0, 0},
		{`sign({v})`, 5, 1},
		{`min({v},3,9)`, 5, 3},
		{`max({v},3,9)`, 5, 9},
		{`clamp({v},0,10)`, 42, 10},
		{`clamp({v},0,10)`, -42, 0},
		{`clamp({v},0,10)`, 7, 7},
		{`hypot({v},4)`, 3, 5},
		{`atan2({v},0)`, 0, 0},
		{`xor({v},5)`, 3, 6},
		{`pow({v},3)`, 2, 8},
		{`bool({v})`, 0, 0},
	}
	for _, c := range cases {
		n, err := parseExpr(c.expr)
		if err != nil {
			t.Errorf("%s: parse: %v", c.expr, err)
			continue
		}
		if err := resolveFuncs(n); err != nil {
			t.Errorf("%s: resolve: %v", c.expr, err)
			continue
		}
		if err := markSelfRefs(n, "v"); err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		got, err := evalExpr(n, &evalCtx{self: c.in})
		if err != nil {
			t.Errorf("%s(%v): %v", c.expr, c.in, err)
			continue
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s(%v) = %v, 期望 %v", c.expr, c.in, got, c.want)
		}
	}
}

// 定义域检查：这几个函数会主动拒绝非法输入，而不是返回 NaN。
func TestBuiltinFuncDomainErrors(t *testing.T) {
	cases := []struct{ expr, want string }{
		{`sqrt({v})`, "sqrt 的参数不能为负"},
		{`ln({v})`, "ln 的参数必须为正"},
		{`log10({v})`, "log10 的参数必须为正"},
		{`log2({v})`, "log2 的参数必须为正"},
	}
	for _, c := range cases {
		n, err := parseExpr(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if err := resolveFuncs(n); err != nil {
			t.Fatal(err)
		}
		if err := markSelfRefs(n, "v"); err != nil {
			t.Fatal(err)
		}
		_, err = evalExpr(n, &evalCtx{self: -1.0})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s(-1) 的错误是 %v，期望含 %q", c.expr, err, c.want)
		}
	}
}

// ---------- 表达式函数：可逆性声明 ----------

func TestBuiltinFuncInvertible(t *testing.T) {
	// 文档承诺可自动求逆的函数，以及推出来的逆式文本。
	ok := map[string]string{
		`sqrt({v})`:  `{.}^2`,
		`cbrt({v})`:  `{.}^3`,
		`exp({v})`:   `ln({.})`,
		`ln({v})`:    `exp({.})`,
		`log10({v})`: `10^{.}`,
		`log2({v})`:  `2^{.}`,
		`sin({v})`:   `asin({.})`,
		`cos({v})`:   `acos({.})`,
		`tan({v})`:   `atan({.})`,
		`asin({v})`:  `sin({.})`,
		`acos({v})`:  `cos({.})`,
		`atan({v})`:  `tan({.})`,
		`pow({v},2)`: `{.}^(1/2)`,
		`bool({v})`:  `{.}`,
	}
	for expr, want := range ok {
		m, err := ParseMapping([]byte(`{"map":{"v":{"key":"p","value":"` + expr + `"}}}`))
		if err != nil {
			t.Errorf("%s 应可自动求逆: %v", expr, err)
			continue
		}
		s, err := New(m)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		if got := s.DerivedReverse("v"); got != want {
			t.Errorf("%s 的逆式是 %q，期望 %q", expr, got, want)
		}
	}

	// 文档声明不可逆的函数：双向规则下必须在保存配置时被拒。
	notOK := []string{
		`abs({v})`, `floor({v})`, `ceil({v})`, `round({v})`, `trunc({v})`,
		`sign({v})`, `min({v},1)`, `max({v},1)`, `clamp({v},0,1)`,
		`hypot({v},1)`, `atan2({v},1)`, `xor({v},1)`, `pow(2,{v})`,
	}
	for _, expr := range notOK {
		_, err := ParseMapping([]byte(`{"map":{"v":{"key":"p","value":"` + expr + `"}}}`))
		if err == nil {
			t.Errorf("%s 不可逆，双向规则应在配置期被拒", expr)
		}
	}
}

// 不可逆函数配上 only 或显式 reverse 后照常可用。
func TestBuiltinFuncOneWayUsable(t *testing.T) {
	s, err := Compile([]byte(`{"map":{"kwh":{"key":"p.e","value":"round({kwh}/10)","only":"b2a"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Uplink([]byte(`{"kwh":237}`))
	if err != nil || string(out) != `{"p":{"e":24}}` {
		t.Errorf("only b2a 上行 = %s, %v", out, err)
	}
	// 另一个方向该属性直接不出现。
	if out, err = s.Downlink([]byte(`{"p":{"e":24}}`)); err != nil || string(out) != "null" {
		t.Errorf("only b2a 下行 = %s, %v", out, err)
	}

	s, err = Compile([]byte(`{"map":{"kwh":{"key":"p.e","value":"round({kwh}/10)","reverse":"{.}*10"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, err = s.Downlink([]byte(`{"p":{"e":24}}`)); err != nil || string(out) != `{"kwh":240}` {
		t.Errorf("显式 reverse 下行 = %s, %v", out, err)
	}
}

// bool() 的输出走 true/false，而不是 1/0。
func TestBuiltinBoolOutput(t *testing.T) {
	s, err := Compile([]byte(`{"map":{"v":{"key":"p.on","value":"bool({v})"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Uplink([]byte(`{"v":3}`)); string(out) != `{"p":{"on":true}}` {
		t.Errorf("bool(3) = %s", out)
	}
	if out, _ := s.Uplink([]byte(`{"v":0}`)); string(out) != `{"p":{"on":false}}` {
		t.Errorf("bool(0) = %s", out)
	}
	// 逆向把 true/false 当 1/0 读回。
	if out, _ := s.Downlink([]byte(`{"p":{"on":true}}`)); string(out) != `{"v":1}` {
		t.Errorf("bool 逆向 = %s", out)
	}
}

func TestExprFuncsListing(t *testing.T) {
	names := ExprFuncs()
	if len(names) == 0 {
		t.Fatal("ExprFuncs 返回空")
	}
	idx := map[string]bool{}
	for _, n := range names {
		idx[n] = true
	}
	// 可逆的带 *，不可逆的不带。
	for _, n := range []string{"sqrt*", "cbrt*", "log2*", "pow*", "bool*", "asin*"} {
		if !idx[n] {
			t.Errorf("ExprFuncs 里缺 %q：%v", n, names)
		}
	}
	for _, n := range []string{"abs", "clamp", "hypot", "xor", "sign"} {
		if !idx[n] {
			t.Errorf("ExprFuncs 里缺 %q", n)
		}
		if idx[n+"*"] {
			t.Errorf("%q 不可逆，不该带 *", n)
		}
	}
	// 排序稳定。
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("ExprFuncs 未排序: %v", names)
		}
	}
}

func TestLookupFunc(t *testing.T) {
	if f, ok := LookupFunc("sqrt"); !ok || f.Name != "sqrt" || f.Arity != 1 || f.Invert == nil {
		t.Errorf("LookupFunc(sqrt) = %+v, %v", f, ok)
	}
	if _, ok := LookupFunc("没有这个函数"); ok {
		t.Error("LookupFunc 对未注册的名字应返回 false")
	}
}

func TestRegisterFuncPanics(t *testing.T) {
	for _, f := range []ExprFunc{
		{Arity: 1, Fn: func([]float64) (float64, error) { return 0, nil }}, // 缺名字
		{Name: "无实现", Arity: 1},                                            // 缺实现
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("RegisterFunc(%+v) 应 panic", f)
				}
			}()
			RegisterFunc(f)
		}()
	}
}

// ---------- 转换器 ----------

func TestBuiltinConverters(t *testing.T) {
	cases := []struct {
		name string
		a    any // 平台侧（数字一侧写成转换器实际返回的类型）
		b    any // 设备侧
	}{
		{"string2int", "42", int64(42)},
		{"string2int", "-67", int64(-67)},
		{"string2float", "1.5", 1.5},
		{"string2bool", "true", true},
		{"int2bool", int64(1), true},
		{"int2bool", int64(0), false},
		{"lower2upper", "ab", "AB"},
		{"unix2rfc3339", int64(1700000000), "2023-11-14T22:13:20Z"},
		{"millis2rfc3339", int64(1700000000000), "2023-11-14T22:13:20Z"},
		{"millis2rfc3339", int64(1700000000123), "2023-11-14T22:13:20.123Z"},
		{"csv2array", "x,y", []any{"x", "y"}},
		{"csv2array", "", []any{}},
		{"json2object", `{"k":1}`, map[string]any{"k": 1.0}},
	}
	for _, c := range cases {
		conv, ok := Lookup(c.name)
		if !ok {
			t.Errorf("Lookup(%q) 失败", c.name)
			continue
		}
		got, err := conv.Forward(c.a)
		if err != nil {
			t.Errorf("%s.Forward(%v): %v", c.name, c.a, err)
		} else if !reflect.DeepEqual(got, c.b) {
			t.Errorf("%s.Forward(%v) = %#v，期望 %#v", c.name, c.a, got, c.b)
		}
		back, err := conv.Reverse(c.b)
		if err != nil {
			t.Errorf("%s.Reverse(%v): %v", c.name, c.b, err)
		} else if !reflect.DeepEqual(back, c.a) {
			t.Errorf("%s.Reverse(%v) = %#v，期望 %#v（往返不一致）", c.name, c.b, back, c.a)
		}
	}
}

func TestBuiltinConverterErrors(t *testing.T) {
	cases := []struct {
		name string
		in   any
		fwd  bool
	}{
		{"string2int", 1.0, true},         // 不是字符串
		{"string2int", "abc", true},       // 不是整数
		{"string2float", "abc", true},     // 不是浮点数
		{"string2bool", "maybe", true},    // 不是布尔
		{"string2bool", 1.0, false},       // 反向需要布尔
		{"int2bool", "x", true},           // 正向需要数字
		{"int2bool", 1.0, false},          // 反向需要布尔
		{"lower2upper", 1.0, true},        // 需要字符串
		{"unix2rfc3339", "x", true},       // 正向需要数字
		{"unix2rfc3339", "不是时间", false},   // 反向需要 RFC3339
		{"millis2rfc3339", "不是时间", false}, // 同上
		{"csv2array", 1.0, true},          // 需要字符串
		{"csv2array", "x", false},         // 反向需要数组
		{"json2object", "{坏的", true},      // 内嵌 JSON 非法
	}
	for _, c := range cases {
		conv, _ := Lookup(c.name)
		fn := conv.Reverse
		dir := "Reverse"
		if c.fwd {
			fn, dir = conv.Forward, "Forward"
		}
		if _, err := fn(c.in); err == nil {
			t.Errorf("%s.%s(%#v) 应报错", c.name, dir, c.in)
		}
	}
}

// csv2array 反向只接受标量；之前用 %v 会把 null 写成 "<nil>"、对象写成 map[k:1]。
func TestCsvArrayReverseScalarsOnly(t *testing.T) {
	conv, _ := Lookup("csv2array")
	got, err := conv.Reverse([]any{"a", nil, 1.5, true, 2.0})
	if err != nil || got != "a,,1.5,true,2" {
		t.Errorf("Reverse = %#v, %v；期望 \"a,,1.5,true,2\"", got, err)
	}
	for _, bad := range []any{
		[]any{map[string]any{"k": 1}}, // 对象没有无歧义的 CSV 表示
		[]any{[]any{1}},               // 嵌套数组同理
		[]any{"a,b"},                  // 含逗号，拼回去会被切错
	} {
		if _, err := conv.Reverse(bad); err == nil {
			t.Errorf("Reverse(%#v) 应报错", bad)
		}
	}
}

// iot_test.go 已覆盖 ~ 前缀的主路径（含带参转换器族），这里补几个边界。
func TestReversePrefixEdgeCases(t *testing.T) {
	if _, ok := Lookup("~~string2int"); !ok {
		t.Error("~~ 相当于原方向，应可解析")
	}
	if _, ok := Lookup("~"); ok {
		t.Error("单个 ~ 不是合法转换器名")
	}
	if _, ok := Lookup("~没有这个转换器"); ok {
		t.Error("未注册的名字加 ~ 仍应失败")
	}
	if _, ok := Lookup("没有这个转换器"); ok {
		t.Error("未注册的名字应返回 false")
	}
}

func TestConvertersListing(t *testing.T) {
	names := Converters()
	idx := map[string]bool{}
	for _, n := range names {
		idx[n] = true
	}
	for _, want := range []string{"string2int", "string2float", "string2bool", "int2bool",
		"lower2upper", "unix2rfc3339", "millis2rfc3339", "csv2array", "json2object"} {
		if !idx[want] {
			t.Errorf("Converters 里缺 %q：%v", want, names)
		}
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Converters 未排序: %v", names)
		}
	}
	// 带参转换器族以 ":" 结尾列出（divide 由 TestConverterFactory 注册）。
	RegisterFactory("listme", func(string) (Converter, error) { return Converter{}, nil })
	found := false
	for _, n := range Converters() {
		if n == "listme:" {
			found = true
		}
	}
	if !found {
		t.Error("Converters 应把转换器族列成 \"listme:\"")
	}
}

func TestRegisterPanicsWithoutName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register 无名转换器应 panic")
		}
	}()
	Register(Converter{})
}

func TestRegisterFactoryPanics(t *testing.T) {
	for _, c := range []struct {
		name string
		f    ConverterFactory
	}{
		{"", func(string) (Converter, error) { return Converter{}, nil }},
		{"有名字没实现", nil},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("RegisterFactory(%q, %v) 应 panic", c.name, c.f)
				}
			}()
			RegisterFactory(c.name, c.f)
		}()
	}
}

func TestShifterMapping(t *testing.T) {
	m := mustMapping(t, `{"dev_sn":"deviceId"}`)
	s, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mapping() != m {
		t.Error("Mapping() 应返回传入的那份映射")
	}
}
