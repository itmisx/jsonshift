package jsonshift

import (
	"reflect"
	"strings"
	"testing"
)

// 用一条规则跑表达式：设备侧 v -> 平台侧 p
func exprMapping(t *testing.T, opts string) *Mapping {
	t.Helper()
	return mustMapping(t, `{"map":{"v":{"key":"p",`+opts+`}}}`)
}

func TestExprEvalAndAutoInverse(t *testing.T) {
	cases := []struct {
		expr    string
		dev     string // 设备侧输入
		plat    string // 上行期望
		inverse string // 自动求出的下行表达式
	}{
		{`{v}/10`, `235`, `23.5`, `{.}*10`},
		{`{v}*3`, `4`, `12`, `{.}/3`},
		{`{v}+40`, `-10`, `30`, `{.}-40`},
		{`{v}-273.15`, `300`, `26.85`, `{.}+273.15`},
		{`({v}-32)*5/9`, `212`, `100`, `(({.}*9)/5)+32`},
		{`100-{v}`, `30`, `70`, `100-{.}`},
		{`1000/{v}`, `4`, `250`, `1000/{.}`},
		{`-{v}`, `5`, `-5`, `-{.}`},
		{`{v}^3`, `3`, `27`, `{.}^(1/3)`},
		{`sqrt({v})`, `16`, `4`, `{.}^2`},
		{`ln({v})`, `1`, `0`, `exp({.})`},
		{`log10({v})`, `1000`, `3`, `10^{.}`},
		{`{v}<<4`, `3`, `48`, `{.}>>4`},
		{`{v}*0x10`, `2`, `32`, `{.}/16`},
		{`{v}/1_000`, `2500`, `2.5`, `{.}*1000`},
		{`{v}*1e3`, `2`, `2000`, `{.}/1000`},
		{`{.}/2`, `9`, `4.5`, `{.}*2`}, // {.} 是自引用的简写
	}
	for _, c := range cases {
		m := exprMapping(t, `"value":"`+c.expr+`"`)
		s, err := New(m)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if got := s.DerivedReverse("v"); got != c.inverse {
			t.Errorf("%s 的逆表达式 = %s，期望 %s", c.expr, got, c.inverse)
		}
		up, err := s.Uplink([]byte(`{"v":` + c.dev + `}`))
		if err != nil {
			t.Errorf("%s 上行: %v", c.expr, err)
			continue
		}
		if string(up) != `{"p":`+c.plat+`}` {
			t.Errorf("%s 上行 = %s，期望 {\"p\":%s}", c.expr, up, c.plat)
			continue
		}
		// 再下行回去必须还原
		down, err := s.Downlink(up)
		if err != nil {
			t.Errorf("%s 下行: %v", c.expr, err)
			continue
		}
		if !reflect.DeepEqual(decode(t, string(down)), decode(t, `{"v":`+c.dev+`}`)) {
			t.Errorf("%s 往返不一致: %s -> %s -> %s", c.expr, c.dev, up, down)
		}
	}
}

// 不可逆的表达式必须在保存配置时就被拒绝，并说明补救办法。
func TestExprRejectedWhenNotInvertible(t *testing.T) {
	cases := []struct{ expr, why string }{
		{`{v}+{other}`, "多处字段引用"},
		{`{v}*{v}`, "多处字段引用"},
		{`abs({v})`, "不可逆函数"},
		{`floor({v}/10)`, "不可逆函数"},
		{`round({v})`, "不可逆函数"},
		{`{v}%256`, "取模"},
		{`{v}>>3`, "右移丢低位"},
		{`{v}&0xFF`, "按位与"},
		{`{v}|1`, "按位或"},
		{`{v}>0?1:0`, "条件表达式"},
		{`min({v},100)`, "不可逆函数"},
		{`42`, "不含字段引用"},
		{`{other}*2`, "引用了别的字段"},
	}
	for _, c := range cases {
		_, err := ParseMapping([]byte(`{"map":{"v":{"key":"p","value":"` + c.expr + `"}}}`))
		if err == nil {
			t.Errorf("%s（%s）应被拒绝", c.expr, c.why)
			continue
		}
		if !strings.Contains(err.Error(), "only") && !strings.Contains(err.Error(), "const") {
			t.Errorf("%s 的报错应给出补救办法，实际: %v", c.expr, err)
		}
	}
}

// 不可逆的表达式配上 only 限定单向，或显式写另一向，就是合法的。
func TestExprOneWayAndExplicitReverse(t *testing.T) {
	// 只上行：设备上报的原始字节里取一位做布尔属性
	one := mustMapping(t, `{"map":{
      "flags": {"key":"properties.alarm", "value":"{flags}>>3&1==1", "only":"b2a"}
    }}`)
	s, err := New(one)
	if err != nil {
		t.Fatal(err)
	}
	up, err := s.Uplink([]byte(`{"flags":8}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(up) != `{"properties":{"alarm":true}}` {
		t.Fatalf("上行 = %s", up)
	}
	// 该规则不参与下行
	down, err := s.Downlink([]byte(`{"properties":{"alarm":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(down) != `null` {
		t.Fatalf("单向规则不应参与下行，得到 %s", down)
	}

	// 显式两向：设备用 0.5kWh 为单位，故意写成不可自动求逆的形式
	both := mustMapping(t, `{"map":{
      "kwh": {"key":"properties.energy", "value":"floor({kwh})/2", "reverse":"{.}*2"}
    }}`)
	s2, err := New(both)
	if err != nil {
		t.Fatal(err)
	}
	if up, _ := s2.Uplink([]byte(`{"kwh":25}`)); string(up) != `{"properties":{"energy":12.5}}` {
		t.Fatalf("上行 = %s", up)
	}
	if dn, _ := s2.Downlink([]byte(`{"properties":{"energy":12.5}}`)); string(dn) != `{"kwh":25}` {
		t.Fatalf("下行 = %s", dn)
	}
}

// 跨字段引用：把设备分两个字节上报的值合成一个平台属性（只能单向）。
func TestExprCrossFieldReference(t *testing.T) {
	m := mustMapping(t, `{"map":{
      "hi": {"key":"properties.raw", "value":"{hi}*256+{lo}", "only":"b2a"}
    }}`)
	s, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	up, err := s.Uplink([]byte(`{"hi":1,"lo":44}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(up) != `{"properties":{"raw":300}}` {
		t.Fatalf("got %s", up)
	}
	if _, err := s.Uplink([]byte(`{"hi":1}`)); err == nil {
		t.Error("缺少被引用的字段应报错")
	}
}

func TestExprBoolResult(t *testing.T) {
	m := exprMapping(t, `"value":"{v}!=0", "only":"b2a"`)
	s, _ := New(m)
	for _, c := range [][2]string{{`0`, `false`}, {`1`, `true`}, {`7`, `true`}} {
		up, err := s.Uplink([]byte(`{"v":` + c[0] + `}`))
		if err != nil {
			t.Fatal(err)
		}
		if string(up) != `{"p":`+c[1]+`}` {
			t.Errorf("v=%s -> %s，期望 %s", c[0], up, c[1])
		}
	}
}

// 设备把数值塞字符串里时，表达式直接接受
func TestExprAcceptsNumericString(t *testing.T) {
	m := exprMapping(t, `"value":"{v}/10"`)
	s, _ := New(m)
	up, err := s.Uplink([]byte(`{"v":"235"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(up) != `{"p":23.5}` {
		t.Fatalf("got %s", up)
	}
	if _, err := s.Uplink([]byte(`{"v":"abc"}`)); err == nil {
		t.Error("非数字字符串应报错")
	}
}

func TestExprWildcard(t *testing.T) {
	// 多通道：表达式逐元素作用
	m := mustMapping(t, `{"map":{
      "ch[*].t": {"key":"properties.channels[*].temperature", "value":"{.}/10"}
    }}`)
	s, _ := New(m)
	up, err := s.Uplink([]byte(`{"ch":[{"t":235},{"t":612}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"properties":{"channels":[{"temperature":23.5},{"temperature":61.2}]}}`
	if string(up) != want {
		t.Fatalf("got %s", up)
	}
	down, _ := s.Downlink([]byte(want))
	if string(down) != `{"ch":[{"t":235},{"t":612}]}` {
		t.Fatalf("下行 = %s", down)
	}
}

func TestExprWildcardRejectsCrossRef(t *testing.T) {
	_, err := ParseMapping([]byte(`{"map":{
      "ch[*].t": {"key":"p[*]", "value":"{.}*{factor}", "only":"b2a"}
    }}`))
	if err == nil {
		t.Fatal("含 [*] 的规则里跨字段引用应被拒绝")
	}
}

func TestExprSyntaxErrors(t *testing.T) {
	bad := []string{
		`{v}/`, `({v}`, `{v})`, `{v}+`, `*{v}`, `{}`, `{v`,
		`{v}@2`, `nosuchfunc({v})`, `sqrt()`, `sqrt({v},2)`, `{v}?1`,
	}
	for _, e := range bad {
		if _, err := ParseMapping([]byte(`{"map":{"v":{"key":"p","value":"` + e + `","only":"b2a"}}}`)); err == nil {
			t.Errorf("表达式 %q 应报语法错", e)
		}
	}
}

func TestExprDivideByZero(t *testing.T) {
	if _, err := ParseMapping([]byte(`{"map":{"v":{"key":"p","value":"{v}/0"}}}`)); err == nil {
		t.Fatal("除以 0 的逆表达式是乘 0，应在编译期或运行期暴露")
	}
	m := exprMapping(t, `"value":"10/{v}", "only":"b2a"`)
	s, _ := New(m)
	if _, err := s.Uplink([]byte(`{"v":0}`)); err == nil {
		t.Error("运行期除以 0 应报错")
	}
}

func TestExprPrecedence(t *testing.T) {
	cases := []struct {
		expr string
		dev  string
		want string
	}{
		{`{v}+2*3`, `1`, `7`},
		{`({v}+2)*3`, `1`, `9`},
		{`-{v}^2`, `3`, `-9`}, // ^ 比一元负号紧
		{`2^3^2`, `0`, `512`}, // ^ 右结合
		{`{v}*2>5`, `3`, `true`},
		{`{v}>1&&{v}<10`, `5`, `true`},
		{`{v}>0?{v}*2:0`, `4`, `8`},
		{`pi>3`, `0`, `true`},
	}
	for _, c := range cases {
		m := exprMapping(t, `"value":"`+c.expr+`", "only":"b2a"`)
		s, err := New(m)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		up, err := s.Uplink([]byte(`{"v":` + c.dev + `}`))
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if string(up) != `{"p":`+c.want+`}` {
			t.Errorf("%s (v=%s) = %s，期望 {\"p\":%s}", c.expr, c.dev, up, c.want)
		}
	}
}

func TestExprMutuallyExclusive(t *testing.T) {
	bad := []string{
		`{"map":{"v":{"key":"p","value":"{v}*2","convert":"string2int"}}}`,
		`{"map":{"v":{"key":"p","value":"{v}*2","enum":[["a",1]]}}}`,
		`{"map":{"v":{"key":"p","convert":"string2int","enum":[["a",1]]}}}`,
	}
	for _, cfg := range bad {
		if _, err := ParseMapping([]byte(cfg)); err == nil {
			t.Errorf("三种值变换机制不能混用: %s", cfg)
		}
	}
}

func TestRegisterCustomFunc(t *testing.T) {
	RegisterFunc(ExprFunc{
		Name:   "halve",
		Arity:  1,
		Fn:     func(a []float64) (float64, error) { return a[0] / 2, nil },
		Invert: func(_ []exprNode, _ int, y exprNode) (exprNode, error) { return bin("*", y, num(2)), nil },
	})
	m := exprMapping(t, `"value":"halve({v})"`)
	s, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.DerivedReverse("v"); got != `{.}*2` {
		t.Fatalf("逆表达式 = %s", got)
	}
	up, _ := s.Uplink([]byte(`{"v":9}`))
	if string(up) != `{"p":4.5}` {
		t.Fatalf("got %s", up)
	}
}

// 后台表单里的空输入框会序列化成 ""，应当等同于“没写表达式”。
func TestExprEmptyMeansNoTransform(t *testing.T) {
	m := mustMapping(t, `{"map":{"v":{"key":"p","value":"","reverse":""}}}`)
	s, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	up, _ := s.Uplink([]byte(`{"v":"原样"}`))
	if string(up) != `{"p":"原样"}` {
		t.Fatalf("got %s", up)
	}
}

// 位运算比比较绑得更紧（照 Go 的优先级），不会掉进 C 的坑。
func TestExprBitwisePrecedence(t *testing.T) {
	m := exprMapping(t, `"value":"{v}&1==1", "only":"b2a"`)
	s, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{`3`, `true`}, {`2`, `false`}} {
		up, err := s.Uplink([]byte(`{"v":` + c[0] + `}`))
		if err != nil {
			t.Fatal(err)
		}
		if string(up) != `{"p":`+c[1]+`}` {
			t.Errorf("v=%s -> %s，期望 %s", c[0], up, c[1])
		}
	}
}

// 乘以 0 不可逆：逆表达式会变成除以 0，静态检查应当拦住。
func TestExprMultiplyByZeroRejected(t *testing.T) {
	if _, err := ParseMapping([]byte(`{"map":{"v":{"key":"p","value":"{v}*0"}}}`)); err == nil {
		t.Fatal("乘以 0 应被拒绝")
	}
}

// 单向不限于上行：只写 reverse + only=a2b 就是只下行的规则。
func TestExprDownlinkOnly(t *testing.T) {
	m := mustMapping(t, `{"map":{
      "v": {"key":"p", "reverse":"round({.}*10)", "only":"a2b"}
    }}`)
	s, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	// 下行：不可逆的 round 也能用，因为不需要反向
	dn, err := s.Downlink([]byte(`{"p":23.46}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(dn) != `{"v":235}` {
		t.Fatalf("下行 = %s", dn)
	}
	// 上行：该规则不参与，属性静默跳过
	up, err := s.Uplink([]byte(`{"v":235}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(up) != `null` {
		t.Fatalf("只下行的规则不应参与上行，得到 %s", up)
	}
}

// 显式 reverse 时引擎不检查往返一致性——有损是配置者的判断，不是报错。
func TestExprExplicitReverseMayBeLossy(t *testing.T) {
	m := mustMapping(t, `{"map":{
      "v": {"key":"p", "value":"floor({v}/10)", "reverse":"{.}*10"}
    }}`)
	s, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	up, _ := s.Uplink([]byte(`{"v":237}`))
	if string(up) != `{"p":23}` {
		t.Fatalf("上行 = %s", up)
	}
	dn, _ := s.Downlink(up)
	if string(dn) != `{"v":230}` { // 个位丢了，库不拦
		t.Fatalf("下行 = %s", dn)
	}
}
