package jsonshift

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// 物模型配置：一份配置同时服务下发与上报。
const thermostatCfg = `{
  "schema": "github.com/itmisx/json-shift",
  "name": "vendor-x-thermostat",
  "map": {
    "dev_sn": "deviceId",
    "temp":   {"key": "properties.temperature", "value": "{temp}/10"},
    "sw_st":  {"key": "properties.powerSwitch", "enum": [["ON", 1], ["OFF", 0]]},
    "md":     {"key": "properties.mode",        "enum": [["cool", 1], ["heat", 2]]}
  }
}`

func TestIoTRoundTrip(t *testing.T) {
	s, err := New(mustMapping(t, thermostatCfg))
	if err != nil {
		t.Fatal(err)
	}
	std := `{"deviceId":"SN-001","properties":{"temperature":23.5,"powerSwitch":"ON","mode":"cool"}}`

	dev, err := s.AToB([]byte(std))
	if err != nil {
		t.Fatal(err)
	}
	if string(dev) != `{"dev_sn":"SN-001","md":1,"sw_st":1,"temp":235}` {
		t.Fatalf("下发结果 = %s", dev)
	}
	back, err := s.BToA(dev)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decode(t, string(back)), decode(t, std)) {
		t.Fatalf("上报还原失败:\ngot  %s\nwant %s", back, std)
	}
}

// 设备通常只上报变化的属性，缺失的属性不应凭空出现在平台数据里。
func TestIoTPartialReport(t *testing.T) {
	s, _ := New(mustMapping(t, thermostatCfg))
	up, err := s.BToA([]byte(`{"temp":218}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(up) != `{"properties":{"temperature":21.8}}` {
		t.Fatalf("部分上报 = %s", up)
	}
}

func TestIoTUnknownEnumValue(t *testing.T) {
	s, _ := New(mustMapping(t, thermostatCfg))
	_, err := s.BToA([]byte(`{"md":9}`))
	if err == nil {
		t.Fatal("未登记的枚举值应报错")
	}
	if !strings.Contains(err.Error(), "md") || !strings.Contains(err.Error(), "枚举") {
		t.Errorf("报错应指明路径与原因: %v", err)
	}
}

// 定点小数：设备以 0.1 为单位传整数，平台存真实量纲。
func TestScaleExpression(t *testing.T) {
	m := mustMapping(t, `{"map":{"v":{"key":"v","value":"{v}/10"}}}`)
	cases := []struct{ dev, plat string }{
		{`235`, `23.5`},
		{`-42`, `-4.2`},
		{`0`, `0`},
		{`7`, `0.7`}, // 规整掉 0.7000000000000001
	}
	for _, c := range cases {
		up, err := Transform([]byte(`{"v":`+c.dev+`}`), m, Uplink)
		if err != nil {
			t.Fatal(err)
		}
		if string(up) != `{"v":`+c.plat+`}` {
			t.Errorf("上行 %s -> %s，期望 %s", c.dev, up, c.plat)
		}
		down, err := Transform([]byte(`{"v":`+c.plat+`}`), m, Downlink)
		if err != nil {
			t.Fatal(err)
		}
		if string(down) != `{"v":`+c.dev+`}` {
			t.Errorf("下行 %s -> %s，期望 %s", c.plat, down, c.dev)
		}
	}
}

// 偏移：设备用无符号整数表示负温。
func TestOffsetExpression(t *testing.T) {
	m := mustMapping(t, `{"map":{"t":{"key":"t","value":"{t}-40"}}}`)
	up, _ := Transform([]byte(`{"t":0}`), m, Uplink)
	if string(up) != `{"t":-40}` {
		t.Fatalf("上行 = %s", up)
	}
	down, _ := Transform([]byte(`{"t":175}`), m, Downlink)
	if string(down) != `{"t":215}` {
		t.Fatalf("下行 = %s", down)
	}
}

// 用户自己注册的带参转换器族：参数非法时的报错要指明是参数问题。
func TestConverterFactory(t *testing.T) {
	RegisterFactory("divide", func(arg string) (Converter, error) {
		d, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			return Converter{}, fmt.Errorf("divide 的参数 %q 不是数字", arg)
		}
		if d == 0 {
			return Converter{}, fmt.Errorf("divide 的除数不能为 0（不可逆）")
		}
		return Converter{
			Forward: func(v any) (any, error) { f, e := toFloat(v); return normalizeNumber(f / d), e },
			Reverse: func(v any) (any, error) { f, e := toFloat(v); return normalizeNumber(f * d), e },
		}, nil
	})

	m := mustMapping(t, `{"map":{"v":{"key":"v","convert":"divide:100"}}}`)
	up, err := Transform([]byte(`{"v":1250}`), m, Uplink)
	if err != nil {
		t.Fatal(err)
	}
	if string(up) != `{"v":125000}` {
		t.Fatalf("got %s", up)
	}

	_, err = ParseMapping([]byte(`{"map":{"v":{"key":"v","convert":"divide:0.1x"}}}`))
	if err == nil {
		t.Fatal("非法参数应报错")
	}
	if !strings.Contains(err.Error(), "0.1x") {
		t.Errorf("报错应指出是参数非法而非转换器不存在: %v", err)
	}
	if _, err := ParseMapping([]byte(`{"map":{"v":{"key":"v","convert":"divide:0"}}}`)); err == nil {
		t.Error("除数 0 不可逆，应报错")
	}
}

func TestEnumTypedValues(t *testing.T) {
	// 枚举两侧都可以是任意 JSON 标量
	m := mustMapping(t, `{"map":{"s":{"key":"s","enum":[[true,"1"],[false,"0"]]}}}`)
	out, _ := Transform([]byte(`{"s":true}`), m, AToB)
	if string(out) != `{"s":"1"}` {
		t.Fatalf("got %s", out)
	}
	back, _ := Transform([]byte(`{"s":"0"}`), m, BToA)
	if string(back) != `{"s":false}` {
		t.Fatalf("got %s", back)
	}
}

func TestEnumOverArray(t *testing.T) {
	m := mustMapping(t, `{"map":{
      "sts": {"key":"items[*].st", "enum":[["ON",1],["OFF",0]]}
    }}`)
	out, _ := Transform([]byte(`{"items":[{"st":"ON"},{"st":"OFF"}]}`), m, AToB)
	if string(out) != `{"sts":[1,0]}` {
		t.Fatalf("got %s", out)
	}
}

// Shifter 预编译后只读，网关会从多个 goroutine 并发调用。
func TestShifterConcurrent(t *testing.T) {
	s, err := New(mustMapping(t, thermostatCfg))
	if err != nil {
		t.Fatal(err)
	}
	in := []byte(`{"deviceId":"SN-001","properties":{"temperature":23.5,"powerSwitch":"ON","mode":"cool"}}`)
	want := `{"dev_sn":"SN-001","md":1,"sw_st":1,"temp":235}`

	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := s.AToB(in)
			if err != nil {
				t.Error(err)
				return
			}
			if string(out) != want {
				t.Errorf("并发结果不一致: %s", out)
			}
			if _, err := s.BToA(out); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestValidateCatchesConfigErrors(t *testing.T) {
	// 后台保存配置时应能挡住这些
	bad := []string{
		`{"map":{}}]}`, // 反向不唯一
		`{"map":{"v":{"key":"v","convert":"divide:"}}}`, // 缺参数
		`{"map":{"v":{"key":"v[*"}}}`,                   // 路径语法错
	}
	for _, cfg := range bad {
		if _, err := ParseMapping([]byte(cfg)); err == nil {
			t.Errorf("应被拦截: %s", cfg)
		}
	}
}

func BenchmarkShifterAToB(b *testing.B) {
	m, err := ParseMapping([]byte(thermostatCfg))
	if err != nil {
		b.Fatal(err)
	}
	s, err := New(m)
	if err != nil {
		b.Fatal(err)
	}
	in := []byte(`{"deviceId":"SN-001","properties":{"temperature":23.5,"powerSwitch":"ON","mode":"cool"}}`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.AToB(in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkShifterTransformValue(b *testing.B) {
	m, _ := ParseMapping([]byte(thermostatCfg))
	s, _ := New(m)
	var src any
	_ = json.Unmarshal([]byte(`{"deviceId":"SN-001","properties":{"temperature":23.5,"powerSwitch":"ON","mode":"cool"}}`), &src)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.TransformValue(src, AToB); err != nil {
			b.Fatal(err)
		}
	}
}

// 设备把数值塞在字符串里上报，平台要数字：内置名字方向是反的，加 ~ 前缀翻过来。
func TestReversePrefix(t *testing.T) {
	m := mustMapping(t, `{"map":{"temp":{"key":"properties.temperature","convert":"~string2int"}}}`)
	up, err := Transform([]byte(`{"temp":"42"}`), m, Uplink)
	if err != nil {
		t.Fatal(err)
	}
	if string(up) != `{"properties":{"temperature":42}}` {
		t.Fatalf("上行 = %s", up)
	}
	down, err := Transform([]byte(`{"properties":{"temperature":42}}`), m, Downlink)
	if err != nil {
		t.Fatal(err)
	}
	if string(down) != `{"temp":"42"}` {
		t.Fatalf("下行 = %s", down)
	}
}

func TestReversePrefixOnFactory(t *testing.T) {
	// ~ 前缀对带参转换器族同样有效
	RegisterFactory("mul", func(arg string) (Converter, error) {
		k, err := strconv.ParseFloat(arg, 64)
		if err != nil || k == 0 {
			return Converter{}, fmt.Errorf("mul 的参数 %q 非法", arg)
		}
		return Converter{
			Forward: func(v any) (any, error) { f, e := toFloat(v); return normalizeNumber(f * k), e },
			Reverse: func(v any) (any, error) { f, e := toFloat(v); return normalizeNumber(f / k), e },
		}, nil
	})
	m := mustMapping(t, `{"map":{"v":{"key":"v","convert":"~mul:10"}}}`)
	out, err := Transform([]byte(`{"v":235}`), m, Downlink)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"v":23.5}` {
		t.Fatalf("got %s", out)
	}
}

func TestReversePrefixOnCustomConverter(t *testing.T) {
	m := &Mapping{
		Fields: []Field{{A: "a", B: "b", Convert: "~lower2upper"}},
	}
	out, err := Transform([]byte(`{"a":"AB"}`), m, Downlink)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"b":"ab"}` {
		t.Fatalf("got %s", out)
	}
}

func TestReversePrefixErrors(t *testing.T) {
	for _, cfg := range []string{
		`{"map":{"v":{"key":"v","convert":"~"}}}`,
		`{"map":{"v":{"key":"v","convert":"~nope"}}}`,
		`{"map":{"v":{"key":"v","convert":"~divide:abc"}}}`,
	} {
		if _, err := ParseMapping([]byte(cfg)); err == nil {
			t.Errorf("应报错: %s", cfg)
		}
	}
}

// 单向转换器配在双向规则上，应在保存配置时就被拦住。
func TestOneWayConverterRejected(t *testing.T) {
	m := &Mapping{
		Fields: []Field{{A: "a", B: "b", Convert: "onlyFwd"}},
		Converters: map[string]Converter{
			"onlyFwd": {Forward: func(v any) (any, error) { return v, nil }},
		},
	}
	if _, err := New(m); err == nil {
		t.Fatal("缺反向实现应报错")
	}
	// 限定只在下行生效时是合法的
	m.Fields[0].Only = ScopeAToB
	if _, err := New(m); err != nil {
		t.Fatalf("only=a2b 时应合法: %v", err)
	}
}
