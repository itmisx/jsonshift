package jsonshift

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func mustMapping(t *testing.T, cfg string) *Mapping {
	t.Helper()
	m, err := ParseMapping([]byte(cfg))
	if err != nil {
		t.Fatalf("ParseMapping: %v", err)
	}
	return m
}

// shift 执行一次转换并把结果解码成 any，便于用 reflect.DeepEqual 比较。
func shift(t *testing.T, in string, m *Mapping, dir Direction) any {
	t.Helper()
	out, err := Transform([]byte(in), m, dir)
	if err != nil {
		t.Fatalf("Transform(%s): %v", dir, err)
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	return v
}

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("测试数据非法: %v", err)
	}
	return v
}

func TestRenameAndNest(t *testing.T) {
	m := mustMapping(t, `{"map":{"user.uid":{"key":"id"},"firstName":{"key":"name.first"},"lastName":{"key":"name.last"}}}`)
	a := `{"id":7,"name":{"first":"张","last":"三"}}`
	b := `{"user":{"uid":7},"firstName":"张","lastName":"三"}`

	if got, want := shift(t, a, m, AToB), decode(t, b); !reflect.DeepEqual(got, want) {
		t.Errorf("A->B = %v, 期望 %v", got, want)
	}
	if got, want := shift(t, b, m, BToA), decode(t, a); !reflect.DeepEqual(got, want) {
		t.Errorf("B->A = %v, 期望 %v", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	m := mustMapping(t, `{"map":{"id":{"key":"meta.id"},"products[*].code":{"key":"items[*].sku"},"products[*].count":{"key":"items[*].qty"},"labels[*]":{"key":"tags[*]"}}}`)
	a := `{"meta":{"id":"x1"},"items":[{"sku":"A","qty":2},{"sku":"B","qty":5}],"tags":["red","blue"]}`

	b, err := Transform([]byte(a), m, AToB)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Transform(b, m, BToA)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decode(t, string(back)), decode(t, a)) {
		t.Fatalf("往返不一致:\n原始 %s\n往返 %s", a, back)
	}
}

func TestWildcardArrays(t *testing.T) {
	m := mustMapping(t, `{"map":{"list[*].uid":{"key":"items[*].id"}}}`)
	got := shift(t, `{"items":[{"id":1},{"id":2},{"id":3}]}`, m, AToB)
	want := decode(t, `{"list":[{"uid":1},{"uid":2},{"uid":3}]}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, 期望 %v", got, want)
	}
}

func TestNestedWildcards(t *testing.T) {
	m := mustMapping(t, `{"map":{"os[*].ls[*].code":{"key":"orders[*].lines[*].sku"}}}`)
	a := `{"orders":[{"lines":[{"sku":"A"},{"sku":"B"}]},{"lines":[{"sku":"C"}]}]}`
	got := shift(t, a, m, AToB)
	want := decode(t, `{"os":[{"ls":[{"code":"A"},{"code":"B"}]},{"ls":[{"code":"C"}]}]}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, 期望 %v", got, want)
	}
}

func TestEmptyArrayMaterialized(t *testing.T) {
	m := mustMapping(t, `{"map":{"list[*].uid":{"key":"items[*].id"}}}`)
	got := shift(t, `{"items":[]}`, m, AToB)
	want := decode(t, `{"list":[]}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("空数组应物化为 []: got %v", got)
	}
}

func TestMissingArrayIsSkipped(t *testing.T) {
	m := mustMapping(t, `{"map":{"list[*].uid":{"key":"items[*].id"}}}`)
	got := shift(t, `{}`, m, AToB)
	if !reflect.DeepEqual(got, decode(t, `null`)) {
		t.Errorf("源端无数组时不应产出字段: got %v", got)
	}
}

func TestFixedIndex(t *testing.T) {
	m := mustMapping(t, `{"map":{"firstID":{"key":"items[0].id"}}}`)
	got := shift(t, `{"items":[{"id":9},{"id":10}]}`, m, AToB)
	if !reflect.DeepEqual(got, decode(t, `{"firstID":9}`)) {
		t.Errorf("got %v", got)
	}
}

func TestDefaultAndRequired(t *testing.T) {
	m := mustMapping(t, `{"map":{"state":{"key":"status","default":"unknown"},"remark":{"key":"note"}}}`)
	got := shift(t, `{}`, m, AToB)
	if !reflect.DeepEqual(got, decode(t, `{"state":"unknown"}`)) {
		t.Errorf("默认值/跳过行为错误: got %v", got)
	}

	req := mustMapping(t, `{"map":{"uid":{"key":"id","required":true}}}`)
	if _, err := Transform([]byte(`{}`), req, AToB); err == nil {
		t.Fatal("缺少必填字段应报错")
	} else if !strings.Contains(err.Error(), "必填") {
		t.Errorf("错误信息不明确: %v", err)
	}
}

func TestConstAndScope(t *testing.T) {
	m := mustMapping(t, `{"map":{"name":{"key":"name"},"type":{"const":"user","only":"a2b"},"":{"key":"source","const":"legacy","only":"b2a"}}}`)
	if got := shift(t, `{"name":"n"}`, m, AToB); !reflect.DeepEqual(got, decode(t, `{"name":"n","type":"user"}`)) {
		t.Errorf("A->B 常量注入错误: got %v", got)
	}
	if got := shift(t, `{"name":"n","type":"user"}`, m, BToA); !reflect.DeepEqual(got, decode(t, `{"name":"n","source":"legacy"}`)) {
		t.Errorf("B->A 常量注入错误: got %v", got)
	}
}

func TestOmitEmpty(t *testing.T) {
	m := mustMapping(t, `{"map":{"x":{"key":"a","omitempty":true},"y":{"key":"b"}}}`)
	got := shift(t, `{"a":"","b":null}`, m, AToB)
	if !reflect.DeepEqual(got, decode(t, `{"y":null}`)) {
		t.Errorf("omitempty 行为错误: got %v", got)
	}
}

func TestConvertersBuiltin(t *testing.T) {
	m := mustMapping(t, `{"map":{"id":{"key":"id","convert":"string2int"},"score":{"key":"score","convert":"string2float"},"on":{"key":"on","convert":"string2bool"},"flag":{"key":"flag","convert":"int2bool"},"code":{"key":"code","convert":"lower2upper"},"ts":{"key":"ts","convert":"unix2rfc3339"},"tags":{"key":"tags","convert":"csv2array"},"payload":{"key":"payload","convert":"json2object"}}}`)
	a := `{"id":"42","score":"1.5","on":"true","flag":1,"code":"ab","ts":0,"tags":"x,y","payload":"{\"k\":1}"}`
	b := shift(t, a, m, AToB)
	want := decode(t, `{"id":42,"score":1.5,"on":true,"flag":true,"code":"AB","ts":"1970-01-01T00:00:00Z","tags":["x","y"],"payload":{"k":1}}`)
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("A->B 转换错误:\ngot  %v\nwant %v", b, want)
	}

	raw, _ := json.Marshal(b)
	back, err := Transform(raw, m, BToA)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decode(t, string(back)), decode(t, a)) {
		t.Fatalf("B->A 往返错误:\ngot  %s\nwant %s", back, a)
	}
}

func TestConvertErrorIsReported(t *testing.T) {
	m := mustMapping(t, `{"map":{"id":{"key":"id","convert":"string2int"}}}`)
	_, err := Transform([]byte(`{"id":"abc"}`), m, AToB)
	if err == nil || !strings.Contains(err.Error(), "string2int") {
		t.Fatalf("应报告转换器失败，得到 %v", err)
	}
}

func TestConvertSkipsNull(t *testing.T) {
	m := mustMapping(t, `{"map":{"id":{"key":"id","convert":"string2int"}}}`)
	got := shift(t, `{"id":null}`, m, AToB)
	if !reflect.DeepEqual(got, decode(t, `{"id":null}`)) {
		t.Errorf("null 应原样透传: got %v", got)
	}
}

func TestCustomConverter(t *testing.T) {
	m := &Mapping{
		Fields: []Field{{A: "cents", B: "yuan", Convert: "cents2yuan"}},
		Converters: map[string]Converter{
			"cents2yuan": {
				Name:    "cents2yuan",
				Forward: func(v any) (any, error) { f, err := toFloat(v); return f / 100, err },
				Reverse: func(v any) (any, error) { f, err := toFloat(v); return int64(f * 100), err },
			},
		},
	}
	out, err := Transform([]byte(`{"cents":1250}`), m, AToB)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"yuan":12.5}` {
		t.Fatalf("got %s", out)
	}
	back, err := Transform(out, m, BToA)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != `{"cents":1250}` {
		t.Fatalf("got %s", back)
	}
}

func TestWildcardInArrayRoot(t *testing.T) {
	m := mustMapping(t, `{"map":{"[*].uid":{"key":"[*].id"}}}`)
	got := shift(t, `[{"id":1},{"id":2}]`, m, AToB)
	if !reflect.DeepEqual(got, decode(t, `[{"uid":1},{"uid":2}]`)) {
		t.Errorf("顶层数组映射错误: got %v", got)
	}
}

func TestShifterReuse(t *testing.T) {
	s, err := New(mustMapping(t, `{"map":{"y":{"key":"x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AToB([]byte(`{"x":1}`))
	if err != nil || string(b) != `{"y":1}` {
		t.Fatalf("AToB = %s, err=%v", b, err)
	}
	a, err := s.BToA(b)
	if err != nil || string(a) != `{"x":1}` {
		t.Fatalf("BToA = %s, err=%v", a, err)
	}
}

func TestTransformValue(t *testing.T) {
	m := mustMapping(t, `{"map":{"y":{"key":"x"}}}`)
	out, err := TransformValue(map[string]any{"x": 1}, m, AToB)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, map[string]any{"y": 1}) {
		t.Fatalf("got %#v", out)
	}
}

func TestValidation(t *testing.T) {
	bad := []string{
		`{"map":{}}`,
		`{"map":{"":{"key":""}}}`,
		`{"map":{"":{"key":"x"}}}`,                                          // 单边且无 const/default
		`{"map":{"y":{"key":"x","only":"sideways"}}}`,                       // only 非法
		`{"map":{"y":{"key":"x","convert":"nope"}}}`,                        // 转换器不存在
		`{"map":{"y":{"key":"a..b"}}}`,                                      // 路径非法
		`{"map":{"y":{"key":"x","typo":1}}}`,                                // 带选项的对象里的未知选项
		`{"map":{"y":{"key":"x","convert":"nosuch:abc"}}}`,                  // 不存在的转换器族
		`{"map":{"y":{"key":"x","convert":"string2int","enum":[["a",1]]}}}`, // convert 与 enum 互斥
		`{"map":{"y":{"key":"x","enum":[["a",1],["a",2]]}}}`,                // 枚举 A 侧重复
		`{"map":{"y":{"key":"x","enum":[["a",1],["b",1]]}}}`,                // 枚举 B 侧重复
	}
	for _, cfg := range bad {
		// 这些都该是合法 JSON，只是语义上不自洽——否则就成了在测 JSON 解析，
		// 而不是在测校验规则（这条断言正是为了防止用例写成坏 JSON 后蒙混过关）。
		if !json.Valid([]byte(cfg)) {
			t.Errorf("用例本身不是合法 JSON，测不到校验逻辑: %s", cfg)
			continue
		}
		if _, err := ParseMapping([]byte(cfg)); err == nil {
			t.Errorf("应校验失败: %s", cfg)
		}
	}
	for _, cfg := range []string{
		`{"map":{"type":{"const":"user"}}}`,     // 单边常量规则
		`{"a":"x"}`,                             // 裸表简写：目标 a <- 源 x
		`{"map":{"ids":{"key":"items[*].id"}}}`, // 两侧通配符数不等 = 聚集
	} {
		if _, err := ParseMapping([]byte(cfg)); err != nil {
			t.Errorf("应校验通过 %s: %v", cfg, err)
		}
	}
}

func TestBadInputJSON(t *testing.T) {
	m := mustMapping(t, `{"map":{"y":{"key":"x"}}}`)
	if _, err := Transform([]byte(`{nope}`), m, AToB); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestNilMapping(t *testing.T) {
	if _, err := Transform([]byte(`{}`), nil, AToB); err == nil {
		t.Fatal("nil 映射应报错")
	}
}

func TestTargetPathConflict(t *testing.T) {
	m := mustMapping(t, `{"map":{"y":{"key":"x"},"y.deep":{"key":"z"}}}`)
	if _, err := Transform([]byte(`{"x":1,"z":2}`), m, AToB); err == nil {
		t.Fatal("目标路径冲突应报错")
	}
}

func BenchmarkTransform(b *testing.B) {
	m, err := ParseMapping([]byte(`{"map":{"id":{"key":"meta.id"},"products[*].code":{"key":"items[*].sku"},"products[*].count":{"key":"items[*].qty"}}}`))
	if err != nil {
		b.Fatal(err)
	}
	in := []byte(`{"meta":{"id":"x"},"items":[{"sku":"A","qty":1},{"sku":"B","qty":2},{"sku":"C","qty":3}]}`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Transform(in, m, AToB); err != nil {
			b.Fatal(err)
		}
	}
}
