package jsonshift

import (
	"reflect"
	"strings"
	"testing"
)

// 配置的两种写法：完整写法（schema / name / map）与省掉外层的简写，
// 以及该被拒掉的形状。

func TestConfigKeyOrderPreserved(t *testing.T) {
	// 后写的规则覆盖先写的，所以键序必须按书写顺序保留
	m := mustMapping(t, `{"x": "a", "x2": "b"}`)
	if m.Fields[0].B != "x" || m.Fields[1].B != "x2" {
		t.Fatalf("键序未保留: %+v", m.Fields)
	}
}

func TestConfigFullForm(t *testing.T) {
	// 同一张 map 表里混着两种值：字符串只改路径，对象带值变换
	m := mustMapping(t, `{
      "name": "mixed",
      "map": {
        "sn": "deviceId",
        "t":  {"key": "properties.temperature", "value": "{t}/10"}
      }
    }`)
	if m.Name != "mixed" || len(m.Fields) != 2 {
		t.Fatalf("解析错误: %+v", m)
	}
	got := shift(t, `{"deviceId":"SN-001","properties":{"temperature":23.5}}`, m, Downlink)
	if !reflect.DeepEqual(got, decode(t, `{"sn":"SN-001","t":235}`)) {
		t.Fatalf("got %v", got)
	}
}

func TestRejectedConfigShapes(t *testing.T) {
	bad := []string{
		`[{"map":{"a":"b"}}]`,              // 配置必须是对象
		`{"map":{"a":{"nested":"x"}}}`,     // 带选项的对象里不认未知字段
		`{"map":{"a":["b","c"]}}`,          // 值必须是路径字符串或对象
		`{"map":{"a":"b"},"unknown":true}`, // 未知顶层字段
		`{"map":{}}`,                       // 空表
		`{"map":{"a":}}`,                   // 非法 JSON
		``,                                 // 空配置
	}
	for _, cfg := range bad {
		if _, err := ParseMapping([]byte(cfg)); err == nil {
			t.Errorf("应报错: %q", cfg)
		}
	}
}

// spec 曾是 map 的同义别名，现在统一成 map，撞到 spec 要给出改名提示而不是
// 当成设备侧字段。
func TestSpecKeyRenamedToMap(t *testing.T) {
	for _, cfg := range []string{
		`{"spec":{"a":"p.a"}}`,                         // 简写位置
		`{"schema":"jsonshift/v1","spec":{"a":"p.a"}}`, // 完整写法位置
		`{"map":{"a":"p.a"},"spec":{"b":"p.b"}}`,       // 两个一起写
	} {
		_, err := ParseMapping([]byte(cfg))
		if err == nil {
			t.Errorf("%s 应报错", cfg)
			continue
		}
		for _, frag := range []string{"spec", "map"} {
			if !strings.Contains(err.Error(), frag) {
				t.Errorf("%s 的报错应提示 %q 的改名: %v", cfg, frag, err)
			}
		}
	}

	// 设备侧字段真叫 spec 时，写进 map 表里照常可用
	s, err := Compile([]byte(`{"map":{"spec":"p.spec"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Uplink([]byte(`{"spec":"v1"}`)); string(out) != `{"p":{"spec":"v1"}}` {
		t.Errorf("设备侧字段叫 spec 时 = %s", out)
	}
}

func TestFromPaths(t *testing.T) {
	m := FromPaths(map[string]string{"sn": "deviceId"})
	got := shift(t, multiChannelInput, m, Downlink)
	if !reflect.DeepEqual(got, decode(t, `{"sn":"SN-001"}`)) {
		t.Fatalf("got %v", got)
	}
	// Go map 无序，构造结果必须按键名排序以保证可复现
	m = FromPaths(map[string]string{"z": "pz", "a": "pa", "m": "pm"})
	var keys []string
	for _, f := range m.Fields {
		keys = append(keys, f.B)
	}
	if !reflect.DeepEqual(keys, []string{"a", "m", "z"}) {
		t.Errorf("FromPaths 未按键排序: %v", keys)
	}
}
