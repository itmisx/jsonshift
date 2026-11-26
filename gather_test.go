package jsonshift

import (
	"reflect"
	"testing"
)

// 数组语义：[*] 成对展开、聚集（源端 [*] 收成数组）、散开（源端数组铺到目标端
// [*]），以及定长下标。重点是**两两互逆**——同一份配置反向跑要能原样还回去。

// 多通道设备：平台用具名嵌套结构，设备用短键 + 数组。
const multiChannelInput = `{
  "deviceId": "SN-001",
  "properties": {
    "channels": [
      {"temperature": 23.5, "label": "进水"},
      {"temperature": 61.2, "label": "回水"}
    ],
    "alarmCodes": [{"code": "E01"}, {"code": "E07"}]
  }
}`

// 简写形式（省掉外层 map）：一行一条只改路径的规则。
const multiChannelPaths = `{
  "sn":        "deviceId",
  "ch[*].t":   "properties.channels[*].temperature",
  "ch[*].n":   "properties.channels[*].label",
  "errs":      "properties.alarmCodes[*].code"
}`

func TestWildcardPairedAndScatter(t *testing.T) {
	m := mustMapping(t, multiChannelPaths)
	got := shift(t, multiChannelInput, m, Downlink)
	want := decode(t, `{
      "sn": "SN-001",
      "ch": [{"t": 23.5, "n": "进水"}, {"t": 61.2, "n": "回水"}],
      "errs": ["E01", "E07"]
    }`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("下行结果不符:\ngot  %v\nwant %v", got, want)
	}
}

// 同一份配置反向跑：聚集（[*] -> 标量）自动变成散开（标量 -> [*]）。
func TestWildcardReversed(t *testing.T) {
	m := mustMapping(t, multiChannelPaths)
	dev, err := Transform([]byte(multiChannelInput), m, Downlink)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Transform(dev, m, Uplink)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decode(t, string(back)), decode(t, multiChannelInput)) {
		t.Fatalf("上行未能还原:\ngot  %s\nwant %s", back, multiChannelInput)
	}
}

func TestFixedIndexPath(t *testing.T) {
	// 设备把两路温度塞进一个定长数组，平台要拆成具名属性
	m := mustMapping(t, `{
      "t[0]": "properties.inletTemp",
      "t[1]": "properties.outletTemp"
    }`)
	dev := shift(t, `{"properties":{"inletTemp":23.5,"outletTemp":61.2}}`, m, Downlink)
	if !reflect.DeepEqual(dev, decode(t, `{"t":[23.5,61.2]}`)) {
		t.Fatalf("下行 got %v", dev)
	}
	std := shift(t, `{"t":[23.5,61.2]}`, m, Uplink)
	if !reflect.DeepEqual(std, decode(t, `{"properties":{"inletTemp":23.5,"outletTemp":61.2}}`)) {
		t.Fatalf("上行 got %v", std)
	}
}

func TestGatherScatterNested(t *testing.T) {
	// 两层聚集：产出嵌套数组，散开能原样还原
	m := mustMapping(t, `{"skus": "orders[*].lines[*].sku"}`)
	a := `{"orders":[{"lines":[{"sku":"A"},{"sku":"B"}]},{"lines":[{"sku":"C"}]}]}`

	b, err := Transform([]byte(a), m, AToB)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"skus":[["A","B"],["C"]]}` {
		t.Fatalf("聚集结果 = %s", b)
	}
	back, err := Transform(b, m, BToA)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decode(t, string(back)), decode(t, a)) {
		t.Fatalf("散开还原失败: %s", back)
	}
}

func TestPartialGather(t *testing.T) {
	// 一侧两层通配、另一侧一层：成对展开一层后再聚集
	m := mustMapping(t, `{"orders[*].skus": "orders[*].lines[*].sku"}`)
	got := shift(t, `{"orders":[{"lines":[{"sku":"A"},{"sku":"B"}]},{"lines":[{"sku":"C"}]}]}`, m, AToB)
	want := decode(t, `{"orders":[{"skus":["A","B"]},{"skus":["C"]}]}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestScatterNeedsArray(t *testing.T) {
	m := mustMapping(t, `{"errs": "properties.alarmCodes[*].code"}`)
	if _, err := Transform([]byte(`{"errs":"notanarray"}`), m, Uplink); err == nil {
		t.Fatal("散开时源端不是数组应报错")
	}
}

func TestExprOverScatter(t *testing.T) {
	// 表达式要逐叶子作用，而不是作用在整个数组上
	m := mustMapping(t, `{"map":{"temps":{"key":"properties.channels[*].temperature","value":"{.}/10"}}}`)
	got := shift(t, `{"properties":{"channels":[{"temperature":23.5},{"temperature":-4.2}]}}`, m, Downlink)
	if !reflect.DeepEqual(got, decode(t, `{"temps":[235,-42]}`)) {
		t.Fatalf("got %v", got)
	}
	back := shift(t, `{"temps":[235,-42]}`, m, Uplink)
	if !reflect.DeepEqual(back, decode(t, `{"properties":{"channels":[{"temperature":23.5},{"temperature":-4.2}]}}`)) {
		t.Fatalf("反向 got %v", back)
	}
}
