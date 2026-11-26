package jsonshift

import (
	"math"
	"strings"
	"testing"
)

// 本文件是一组回归测试，每个用例对应一个曾经存在的缺陷。
// 命名里的编号与 review 清单对应，便于回溯。

// 1) NaN / ±Inf 曾经逃出 Validate()：Transform 会在最后序列化时以一句不带规则
// 定位的错误失败，而 TransformValue 更糟——静默返回一个无法序列化的值。
func TestRegressNaNInfRejected(t *testing.T) {
	cases := []struct {
		expr string
		in   float64
		want string // 期望的特殊值
		text string // 报错里回显的表达式文本（空则与 expr 相同）
	}{
		{expr: `acos({v})`, in: 4, want: "NaN"},    // 超出定义域
		{expr: `asin({v})`, in: 4, want: "NaN"},    // 同上
		{expr: `{v}^0.5`, in: -4, want: "NaN"},     // 负底数的分数次幂
		{expr: `exp({v})`, in: 1000, want: "+Inf"}, // 溢出
		// 极端量级的字面量按科学计数法回显，不铺成 309 位数字
		{expr: `{v}*1e308`, in: 1e10, want: "+Inf", text: `{v}*1e+308`},
		{expr: `{v}*1e308`, in: -1e10, want: "-Inf", text: `{v}*1e+308`},
	}
	for _, c := range cases {
		s, err := New(&Mapping{Fields: []Field{{A: "p.x", B: "v", Value: c.expr, Reverse: "{.}"}}})
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		// TransformValue 必须报错，而不是把 NaN/Inf 交给调用方。
		got, err := s.TransformValue(map[string]any{"v": c.in}, BToA)
		if err == nil {
			t.Errorf("%s(%v) 应报错，却返回了 %#v", c.expr, c.in, got)
			continue
		}
		text := c.text
		if text == "" {
			text = c.expr
		}
		for _, frag := range []string{c.want, `map["v"]`, text} {
			if !strings.Contains(err.Error(), frag) {
				t.Errorf("%s(%v) 的报错 %q 应含 %q", c.expr, c.in, err, frag)
			}
		}
		// 字节入口同理，且报错同样带规则定位。
		if _, err := s.Uplink([]byte(`{"v":` + formatNumber(c.in) + `}`)); err == nil ||
			!strings.Contains(err.Error(), `map["v"]`) {
			t.Errorf("%s(%v) Uplink 的报错 = %v", c.expr, c.in, err)
		}
	}

	// 正常范围内照常工作，没被误伤。
	s, err := Compile([]byte(`{"map":{"v":{"key":"p.x","value":"asin({v})"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := s.Uplink([]byte(`{"v":0}`)); err != nil || string(out) != `{"p":{"x":0}}` {
		t.Errorf("asin(0) = %s, %v", out, err)
	}
}

// 2) 省掉外层写法时，schema / name 曾被当成设备侧字段静默编成规则。
func TestRegressShorthandReservedKeys(t *testing.T) {
	for _, cfg := range []string{
		`{"schema":"jsonshift/v1","name":"n"}`,
		`{"schema":"github.com/itmisx/json-shift","dev_sn":"deviceId"}`,
		`{"name":"vendor-x","dev_sn":"deviceId"}`,
	} {
		m, err := ParseMapping([]byte(cfg))
		if err == nil {
			t.Errorf("%s 应报错（缺 map 表），却编出了 %+v", cfg, m.Fields)
			continue
		}
		if !strings.Contains(err.Error(), "map") {
			t.Errorf("%s 的报错应提示补 map 表: %v", cfg, err)
		}
	}

	// 完整写法当然照常，schema 也要参与校验。
	if _, err := ParseMapping([]byte(
		`{"schema":"jsonshift/v1","name":"n","map":{"dev_sn":"deviceId"}}`)); err != nil {
		t.Errorf("完整写法应通过: %v", err)
	}
	if _, err := ParseMapping([]byte(
		`{"schema":"别的格式","map":{"dev_sn":"deviceId"}}`)); err == nil {
		t.Error("schema 取值非法应报错")
	}

	// 设备侧字段真叫 schema/name 时，用完整写法仍可表达。
	s, err := Compile([]byte(`{"map":{"schema":"p.schema","name":"p.name"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Uplink([]byte(`{"schema":"s","name":"n"}`)); string(out) != `{"p":{"name":"n","schema":"s"}}` {
		t.Errorf("设备侧字段叫 schema/name 时 = %s", out)
	}
}

// 3) const 不读源端，再配值变换是死配置——曾经静默把 value/enum/convert 丢掉。
func TestRegressConstExcludesValueMap(t *testing.T) {
	for _, opts := range []string{
		`"const":7,"value":"{v}/10"`,
		`"const":7,"reverse":"{.}*10"`,
		`"const":7,"enum":[["a",1]]`,
		`"const":7,"convert":"string2int"`,
	} {
		_, err := ParseMapping([]byte(`{"map":{"v":{"key":"p.v",` + opts + `}}}`))
		if err == nil {
			t.Errorf(`{%s} 应报错`, opts)
			continue
		}
		if !strings.Contains(err.Error(), "const") {
			t.Errorf(`{%s} 的报错应点明 const: %v`, opts, err)
		}
	}

	// const 单用、以及 default 配值变换（default 是目标侧字面量，不经过变换）都合法。
	if _, err := ParseMapping([]byte(`{"map":{"v":{"key":"p.v","const":7,"only":"b2a"}}}`)); err != nil {
		t.Errorf("const 单用应通过: %v", err)
	}
	s, err := Compile([]byte(`{"map":{"v":{"key":"p.v","value":"{v}/10","default":0}}}`))
	if err != nil {
		t.Fatalf("default + value 应通过: %v", err)
	}
	if out, _ := s.Uplink([]byte(`{"v":218}`)); string(out) != `{"p":{"v":21.8}}` {
		t.Errorf("有值时走表达式 = %s", out)
	}
	if out, _ := s.Uplink([]byte(`{}`)); string(out) != `{"p":{"v":0}}` {
		t.Errorf("缺值时走 default（不经表达式） = %s", out)
	}
}

// 4) 显式写 default: null / const: null 曾等于没写（按 != nil 判断存在性）。
func TestRegressExplicitNullOption(t *testing.T) {
	// 写了 default: null → 缺值时写入 null
	s, err := Compile([]byte(`{"map":{"v":{"key":"p.v","default":null}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Uplink([]byte(`{}`)); string(out) != `{"p":{"v":null}}` {
		t.Errorf(`default:null 缺值 = %s，期望 {"p":{"v":null}}`, out)
	}
	// 没写 default → 缺值整条跳过
	s2, err := Compile([]byte(`{"map":{"v":{"key":"p.v"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s2.Uplink([]byte(`{}`)); string(out) != `null` {
		t.Errorf("不写 default 缺值 = %s，期望 null", out)
	}
	// const: null
	s3, err := Compile([]byte(`{"map":{"v":{"key":"p.v","const":null,"only":"b2a"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s3.Uplink([]byte(`{}`)); string(out) != `{"p":{"v":null}}` {
		t.Errorf("const:null = %s", out)
	}
	// 单边规则里 default: null 是合法的“提供了 default”，不该再报缺 const/default
	if _, err := ParseMapping([]byte(`{"map":{"ver":{"key":"","default":null}}}`)); err != nil {
		t.Errorf("单边规则 + default:null 应通过: %v", err)
	}

	// Go 侧构造：Const/Default 非 nil 时无需 Has*，保持向后兼容
	s4, err := New(&Mapping{Fields: []Field{{A: "p.v", B: "v", Default: 5.0}}})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s4.Uplink([]byte(`{}`)); string(out) != `{"p":{"v":5}}` {
		t.Errorf("Go 侧 Default 不带 HasDefault = %s", out)
	}
	s5, err := New(&Mapping{Fields: []Field{{A: "p.v", B: "v", HasDefault: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s5.Uplink([]byte(`{}`)); string(out) != `{"p":{"v":null}}` {
		t.Errorf("Go 侧 HasDefault = %s", out)
	}
}

// 5) 表达式里引用含 [*] 的路径曾能过 Validate()，但每次转换都必然失败。
func TestRegressWildcardRefRejected(t *testing.T) {
	for _, opts := range []string{
		`"value":"{ch[*].t}*2","reverse":"{.}"`,
		`"value":"{.}*2","reverse":"{p.c[*].t}"`,
		`"value":"{a[*]}+{b}","reverse":"{.}"`,
	} {
		_, err := ParseMapping([]byte(`{"map":{"v":{"key":"p.v",` + opts + `}}}`))
		if err == nil {
			t.Errorf(`{%s} 含 [*] 引用，应在配置期被拒`, opts)
			continue
		}
		if !strings.Contains(err.Error(), "[*]") {
			t.Errorf(`{%s} 的报错应点明 [*]: %v`, opts, err)
		}
	}
	// 不含通配符的跨字段引用照常可用。
	s, err := Compile([]byte(`{"map":{"hi":{"key":"p.v","value":"{hi}*256+{lo}","reverse":"{.}"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Uplink([]byte(`{"hi":1,"lo":2}`)); string(out) != `{"p":{"v":258}}` {
		t.Errorf("跨字段引用 = %s", out)
	}
	// 含 [*] 的规则里用 {.} 照常可用。
	if _, err := ParseMapping([]byte(
		`{"map":{"ch[*].t":{"key":"p.c[*].t","value":"{.}/10"}}}`)); err != nil {
		t.Errorf("[*] 规则 + {.} 应通过: %v", err)
	}
}

// 6) 顶层就是数组时，空数组曾退化成 null（materializeEmpty 对空前缀误判为已存在）。
func TestRegressTopLevelEmptyArray(t *testing.T) {
	s, err := Compile([]byte(`{"map":{"[*].t":"[*].x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := s.Uplink([]byte(`[]`)); err != nil || string(out) != `[]` {
		t.Errorf("顶层空数组 = %s, %v；期望 []", out, err)
	}
	if out, err := s.Uplink([]byte(`[{"t":1},{"t":2}]`)); err != nil || string(out) != `[{"x":1},{"x":2}]` {
		t.Errorf("顶层非空数组 = %s, %v", out, err)
	}
	// 嵌套位置本来就对，确认没被改坏。
	s2, err := Compile([]byte(`{"map":{"ch[*].t":"p.c[*].t"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s2.Uplink([]byte(`{"ch":[]}`)); string(out) != `{"p":{"c":[]}}` {
		t.Errorf("嵌套空数组 = %s", out)
	}
	// 散开方向的空数组同理。
	s3, err := Compile([]byte(`{"map":{"errs":"p.alarms[*].code"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s3.Uplink([]byte(`{"errs":[]}`)); string(out) != `{"p":{"alarms":[]}}` {
		t.Errorf("散开空数组 = %s", out)
	}
}

// 7) enumKey 曾把 float32 宽化成 float64（1.1 → 1.100000023841858），永远匹配不上；
// 无符号整型也没列进去。
func TestRegressEnumNumericTypes(t *testing.T) {
	m := &Mapping{Fields: []Field{{A: "p.x", B: "x", Enum: [][2]any{
		{"f32", float32(1.1)},
		{"u8", uint8(2)},
		{"u64", uint64(3)},
		{"i16", int16(4)},
		{"i", 5},
		{"f64", 6.5},
	}}}}
	s, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{
		{`{"x":1.1}`, `{"p":{"x":"f32"}}`},
		{`{"x":2}`, `{"p":{"x":"u8"}}`},
		{`{"x":3}`, `{"p":{"x":"u64"}}`},
		{`{"x":4}`, `{"p":{"x":"i16"}}`},
		{`{"x":5}`, `{"p":{"x":"i"}}`},
		{`{"x":6.5}`, `{"p":{"x":"f64"}}`},
	} {
		out, err := s.Uplink([]byte(c[0]))
		if err != nil || string(out) != c[1] {
			t.Errorf("Uplink(%s) = %s, %v；期望 %s", c[0], out, err, c[1])
		}
	}
	// 不同数值类型表示同一个数时，应被判为重复而不是两个条目。
	if _, err := New(&Mapping{Fields: []Field{{A: "p.x", B: "x", Enum: [][2]any{
		{"a", 1}, {"b", 1.0},
	}}}}); err == nil {
		t.Error("设备侧值 1 与 1.0 是同一个数，应判重复")
	}
}

// 8) csv2array 反向曾用 %v，把 null 写成字面量 "<nil>"、对象写成 Go 的 map[k:1]。
//    （断言在 builtin_test.go 的 TestCsvArrayReverseScalarsOnly 里）

//  9. 表达式函数曾在每次求值时查全局注册表：既是热路径上的读锁，也让已编译的
//     Shifter 会被之后的 RegisterFunc 改变行为。
func TestRegressFuncsResolvedAtCompileTime(t *testing.T) {
	s, err := Compile([]byte(`{"map":{"v":{"key":"p.v","value":"sqrt({v})"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Uplink([]byte(`{"v":16}`))
	if string(before) != `{"p":{"v":4}}` {
		t.Fatalf("编译后 = %s", before)
	}

	orig, _ := LookupFunc("sqrt")
	RegisterFunc(ExprFunc{Name: "sqrt", Arity: 1,
		Fn: func([]float64) (float64, error) { return 999, nil }})
	defer RegisterFunc(orig)

	after, _ := s.Uplink([]byte(`{"v":16}`))
	if string(after) != string(before) {
		t.Errorf("覆盖同名函数后已编译 Shifter 的行为变了: %s -> %s", before, after)
	}
	// 新编译的 Shifter 才看到新实现。
	s2, err := Compile([]byte(`{"map":{"v":{"key":"p.v","value":"sqrt({v})","reverse":"{.}"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s2.Uplink([]byte(`{"v":16}`)); string(out) != `{"p":{"v":999}}` {
		t.Errorf("新编译的 Shifter 应用新实现，得到 %s", out)
	}
}

// 自动求逆造出来的函数调用（如 ln/exp）也必须在编译期解析好。
func TestRegressInvertedCallsResolved(t *testing.T) {
	s, err := Compile([]byte(`{"map":{"v":{"key":"p.v","value":"2^{v}"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.DerivedReverse("v"); got != `ln({.})/ln(2)` {
		t.Fatalf("逆式 = %q", got)
	}
	if out, err := s.Downlink([]byte(`{"p":{"v":8}}`)); err != nil || string(out) != `{"v":3}` {
		t.Errorf("走自动求逆出来的 ln() = %s, %v", out, err)
	}
}

// 10) 运行期报错曾用 fields[i]，与配置期的 map["键"] 不一致。
func TestRegressRuntimeErrorLocator(t *testing.T) {
	s, err := Compile([]byte(`{"map":{"md":{"key":"p.mode","enum":[["cool",1]]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Uplink([]byte(`{"md":9}`))
	if err == nil {
		t.Fatal("未登记的枚举值应报错")
	}
	for _, frag := range []string{`map["md"]`, "md -> p.mode", "b2a"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("上行报错 %q 应含 %q", err, frag)
		}
	}
	if _, err = s.Downlink([]byte(`{"p":{"mode":"warm"}}`)); err == nil ||
		!strings.Contains(err.Error(), "p.mode -> md") {
		t.Errorf("下行报错应按方向渲染路径: %v", err)
	}
	// 配置期用的是同一个键。
	_, cerr := ParseMapping([]byte(`{"map":{"md":{"key":"p.mode","enum":[["a",1],["b",1]]}}}`))
	if cerr == nil || !strings.Contains(cerr.Error(), `map["md"]`) {
		t.Errorf("配置期报错 = %v", cerr)
	}
}

// 11) only 与 value/reverse 的搭配曾不校验：写错那一边会被悄悄求逆成相反的含义，
// 报错还会建议「用 only 限定成单向规则」——而用户已经写了 only。
func TestRegressOnlyDirectionMatchesExpr(t *testing.T) {
	bad := []struct{ opts, want string }{
		{`"value":"round({v}/10)","only":"a2b"`, "reverse"},             // 只下行，却只写了 value
		{`"reverse":"{.}*10","only":"b2a"`, "value"},                    // 只上行，却只写了 reverse
		{`"value":"{v}/10","reverse":"{.}*10","only":"b2a"`, "reverse"}, // reverse 是死配置
		{`"value":"{v}/10","reverse":"{.}*10","only":"a2b"`, "value"},   // value 是死配置
	}
	for _, c := range bad {
		_, err := ParseMapping([]byte(`{"map":{"v":{"key":"p.v",` + c.opts + `}}}`))
		if err == nil {
			t.Errorf(`{%s} 应报错`, c.opts)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf(`{%s} 的报错应点明 %q: %v`, c.opts, c.want, err)
		}
		// 不该再出现那句误导性的建议。
		if strings.Contains(err.Error(), "用 only 限定成单向规则") {
			t.Errorf(`{%s} 已经写了 only，报错不该再建议用 only: %v`, c.opts, err)
		}
	}

	// README 里的两种正确搭配照常通过，且只在该方向生效。
	s, err := Compile([]byte(`{"map":{"flags":{"key":"p.alarm","value":"{flags}>>3&1==1","only":"b2a"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Uplink([]byte(`{"flags":8}`)); string(out) != `{"p":{"alarm":true}}` {
		t.Errorf("value + only b2a 上行 = %s", out)
	}
	if out, _ := s.Downlink([]byte(`{"p":{"alarm":true}}`)); string(out) != `null` {
		t.Errorf("value + only b2a 下行应静默跳过，得到 %s", out)
	}

	s2, err := Compile([]byte(`{"map":{"spd":{"key":"p.speed","reverse":"round({.}*255/100)","only":"a2b"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s2.Downlink([]byte(`{"p":{"speed":50}}`)); string(out) != `{"spd":128}` {
		t.Errorf("reverse + only a2b 下行 = %s", out)
	}
	if out, _ := s2.Uplink([]byte(`{"spd":128}`)); string(out) != `null` {
		t.Errorf("reverse + only a2b 上行应静默跳过，得到 %s", out)
	}
}

// 12) Compile 一次解析 + 一次编译，与 ParseMapping + New 等价。
func TestRegressCompileEntry(t *testing.T) {
	const cfg = `{"map":{"temp":{"key":"p.t","value":"{temp}/10"}}}`
	s, err := Compile([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseMapping([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Uplink([]byte(`{"temp":218}`))
	b, _ := s2.Uplink([]byte(`{"temp":218}`))
	if string(a) != string(b) || string(a) != `{"p":{"t":21.8}}` {
		t.Errorf("Compile=%s ParseMapping+New=%s", a, b)
	}
	// 配置有错时 Compile 也要在这一步报出来。
	if _, err := Compile([]byte(`{"map":{"v":{"key":"p.v","value":"{v}%256"}}}`)); err == nil {
		t.Error("Compile 应校验配置")
	}
	if _, err := Compile([]byte(`{坏的`)); err == nil {
		t.Error("Compile 应拒绝非法 JSON")
	}
}

// 超出 int64 的浮点数不该依赖溢出转换的实现定义行为。
func TestRegressLargeNumberFormatting(t *testing.T) {
	for _, f := range []float64{1e19, 1e20, 1e30, -1e19, math.MaxFloat64} {
		if got := normalizeNumber(f); got == int64(0) {
			t.Errorf("normalizeNumber(%v) = %v", f, got)
		}
		if s := formatNumber(f); s == "" || strings.HasPrefix(s, "-922337") {
			t.Errorf("formatNumber(%v) = %q", f, s)
		}
	}
	// 安全范围内仍按整数输出。
	if got := normalizeNumber(42); got != int64(42) {
		t.Errorf("normalizeNumber(42) = %#v", got)
	}
	if s := formatNumber(42); s != "42" {
		t.Errorf("formatNumber(42) = %q", s)
	}
}
