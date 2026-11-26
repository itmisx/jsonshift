package jsonshift

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Direction 指定本次转换的方向。
type Direction int

const (
	AToB Direction = iota // 平台 -> 设备：读 a 路径的值，写入 b 路径
	BToA                  // 设备 -> 平台：读 b 路径的值，写入 a 路径
)

// 面向 IoT 场景的方向别名：下行是把平台标准格式转发给设备，上行是把设备上报
// 的数据转回平台标准格式。
const (
	Downlink = AToB // 下发给设备
	Uplink   = BToA // 设备上报
)

func (d Direction) String() string {
	if d == BToA {
		return "b2a"
	}
	return "a2b"
}

// Scope 限定某条字段规则在哪些方向生效。
type Scope string

const (
	ScopeBoth Scope = "both" // 默认：双向生效
	ScopeAToB Scope = "a2b"  // 只在 a -> b 生效
	ScopeBToA Scope = "b2a"  // 只在 b -> a 生效
)

// Field 是一条属性映射规则：平台侧的 a 路径与设备侧的 b 路径互为对应。
// map 表里的一行 `"设备侧路径": "平台侧路径"` 等价于 Field{A: 平台侧, B: 设备侧}。
type Field struct {
	A string `json:"a"` // 平台侧路径，如 "properties.temperature"
	B string `json:"b"` // 设备侧路径，如 "temp"、"ch[*].t"

	// Const 不读源端，直接把常量写入目标端（常配合 Only 使用）。
	Const any `json:"const,omitempty"`
	// HasConst 让 Const 能表达 JSON null。Const 非 nil 时无需设置；
	// 只有“常量就是 null”这一种情形才需要显式置为 true。
	HasConst bool `json:"hasConst,omitempty"`
	// Default 在源端路径缺失时写入目标端；不设置则跳过该字段。
	Default any `json:"default,omitempty"`
	// HasDefault 让 Default 能表达 JSON null，用法同 HasConst。
	HasDefault bool `json:"hasDefault,omitempty"`
	// Required 为 true 时，源端缺失该路径会返回错误。
	Required bool `json:"required,omitempty"`
	// Value 是上行表达式：平台值 = f(设备值)，可引用 {.}（本字段源值）、
	// {自己的设备侧路径} 或其它绝对路径。只含加减乘除、括号与一元负号。
	Value string `json:"value,omitempty"`
	// Reverse 是下行表达式。留空时由 Value 自动求逆——这要求 Value 里只出现
	// 一次自引用；否则必须显式给出，或用 Only 限定成单向规则。
	Reverse string `json:"reverse,omitempty"`
	// Convert 是转换器名，Forward 用于 a->b，Reverse 用于 b->a。
	// 支持带参数的写法，如 "scale:0.1"、"offset:-40"。
	Convert string `json:"convert,omitempty"`
	// Enum 是值层面的双向映射表：[[A侧值, B侧值], ...]，
	// 用于平台枚举与厂商取值之间互转（如 ["ON", 1]）。与 Convert 互斥。
	Enum [][2]any `json:"enum,omitempty"`
	// Only 限定生效方向，默认 both。
	Only Scope `json:"only,omitempty"`
	// OmitEmpty 为 true 时，空值（null、""、空数组、空对象）不写入目标端。
	OmitEmpty bool `json:"omitempty,omitempty"`
	// Comment 仅作说明，不参与转换。
	Comment string `json:"comment,omitempty"`
}

// hasConst / hasDefault 判断选项是否“写了”，而不是“非 nil”——配置里显式写
// default: null 表示“缺值时写入 null”，与完全不写 default（缺值即跳过）不同。
func (f *Field) hasConst() bool   { return f.HasConst || f.Const != nil }
func (f *Field) hasDefault() bool { return f.HasDefault || f.Default != nil }

func (f *Field) applies(dir Direction) bool {
	switch f.Only {
	case "", ScopeBoth:
		return true
	case ScopeAToB:
		return dir == AToB
	case ScopeBToA:
		return dir == BToA
	default:
		return false
	}
}

// key 返回这条规则在 map 表里的键（即设备侧路径），仅 A 侧的规则退回 A。
// 配置期与运行期的报错都用它定位，和用户书写的形式一致。
func (f *Field) key() string {
	if f.B != "" {
		return f.B
	}
	return f.A
}

// route 按方向渲染“源路径 -> 目标路径”，用于运行期报错。
func (f *Field) route(dir Direction) string {
	src, dst := f.A, f.B
	if dir == BToA {
		src, dst = f.B, f.A
	}
	switch {
	case src == "":
		return "-> " + dst
	case dst == "":
		return src + " ->"
	}
	return src + " -> " + dst
}

// Mapping 是整套映射关系，可由 JSON 解析，也可用 Go 字面量构造。
type Mapping struct {
	// Schema 标识配置格式，便于后台存多版本配置时做迁移；可留空。
	Schema string  `json:"schema,omitempty"`
	Name   string  `json:"name,omitempty"`
	Fields []Field `json:"map"`

	// Converters 是本映射私有的转换器，优先于全局注册表；
	// 其中的函数无法由 JSON 表达，只能在 Go 代码里挂上。
	Converters map[string]Converter `json:"-"`
}

// 配置格式标识。可留空；填错则在解析阶段报错，避免把别的格式喂进来。
const (
	SchemaModulePath = "github.com/itmisx/jsonshift"
	SchemaV1         = "jsonshift/v1"
)

// ParseMapping 解析后台配置的映射关系。完整写法是一张 map 表，
// 键是设备侧路径，值可以是平台侧路径（只改路径），也可以是带选项的对象：
//
//	{
//	  "schema": "github.com/itmisx/jsonshift",
//	  "name":   "vendor-x-thermostat",
//	  "map": {
//	    "dev_sn": "deviceId",
//	    "temp":   {"key": "properties.temperature", "value": "{temp}/10"},
//	    "sw_st":  {"key": "properties.powerSwitch", "enum": [["ON", 1], ["OFF", 0]]}
//	  }
//	}
//
// 键是设备侧（B）路径，值里的 key 是平台侧（A）路径，从左到右读正好是上行的
// 流向：设备的这个字段，对应平台的那个字段。下行把两侧互换并对表达式求逆，
// 无需另写一份配置。
//
// 只有一张表时外层可以省掉，直接写 {"dev_sn":"deviceId"}；但设备侧字段名正好叫
// map、spec、schema 或 name 时必须用完整写法（它们是外层字段名，会被优先识别）。
// 键的书写顺序被保留——同一目标路径被多条规则命中时，后者覆盖前者。
func ParseMapping(data []byte) (*Mapping, error) {
	m, err := parseMapping(data)
	if err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// parseMapping 只做解析，不做语义校验——校验等于编译一遍，交给调用方按需触发：
// ParseMapping 走 Validate，Compile 直接走 New，各只编译一次。
func parseMapping(data []byte) (*Mapping, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("jsonshift: 映射内容为空")
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("jsonshift: 映射配置必须是 JSON 对象")
	}
	pairs, err := decodeOrderedObject(trimmed)
	if err != nil {
		return nil, fmt.Errorf("jsonshift: 解析映射失败: %w", err)
	}
	m, err := parseConfig(pairs)
	if err != nil {
		return nil, fmt.Errorf("jsonshift: 解析映射失败: %w", err)
	}
	return m, nil
}

// FromPaths 用 Go map 构造只改路径的映射（键为设备侧路径、值为平台侧路径），
// 相当于 map 表的简写形式。
// Go map 无序，这里按键名排序以保证行为可复现；若规则之间存在覆盖关系，
// 请改用 ParseMapping 或直接构造 Fields 以控制顺序。
func FromPaths(paths map[string]string) *Mapping {
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	m := &Mapping{Fields: make([]Field, 0, len(keys))}
	for _, k := range keys {
		m.Fields = append(m.Fields, Field{A: paths[k], B: k})
	}
	return m
}

type rawPair struct {
	key string
	val json.RawMessage
}

// decodeOrderedObject 按书写顺序解码一个 JSON 对象的键值对。
func decodeOrderedObject(data []byte) ([]rawPair, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("期望 JSON 对象")
	}
	var out []rawPair
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, fmt.Errorf("非法的对象键 %v", kt)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, rawPair{key, raw})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

// mapEntry 是 map 表里带选项的那种值。字段与 Field 一一对应，
// 只是把“设备侧路径”从对象里挪到了表的键上。
type mapEntry struct {
	Key       string          `json:"key"`
	Value     string          `json:"value,omitempty"`
	Reverse   string          `json:"reverse,omitempty"`
	Convert   string          `json:"convert,omitempty"`
	Enum      [][2]any        `json:"enum,omitempty"`
	Const     json.RawMessage `json:"const,omitempty"`
	Default   json.RawMessage `json:"default,omitempty"`
	Required  bool            `json:"required,omitempty"`
	Only      Scope           `json:"only,omitempty"`
	OmitEmpty bool            `json:"omitempty,omitempty"`
	Comment   string          `json:"comment,omitempty"`
}

// errSpecRenamed 是 spec -> map 的改名提示。曾经两个名字都收，但一个字段两种
// 拼法会让后台存下同义而不同形的配置，与本库「拼错不被静默忽略」的取向相悖；
// 而且 spec 在 IoT 语境里指物模型本身（TSL, Thing Specification Language），
// 用来命名一张路径映射表是误导。
var errSpecRenamed = fmt.Errorf(
	`顶层字段 "spec" 已改名为 "map"：把 "spec": {...} 改成 "map": {...} 即可` +
		`（设备侧字段确实叫 spec 时，把它写进 map 表里）`)

// parseConfig 识别完整写法与省略外层的简写。
func parseConfig(pairs []rawPair) (*Mapping, error) {
	var table []rawPair
	m := &Mapping{}
	wrapped := false
	for _, p := range pairs {
		if p.key == "map" {
			wrapped = true
		}
	}
	if !wrapped {
		// 没有 map 键时按简写处理，但撞上外层字段名几乎总意味着用户想写完整
		// 形式却漏了（或写错了）map 表——此时静默当成设备侧字段会编出一套完全
		// 错误的规则，所以这里直接报错。
		// spec 先查一遍：它的提示（改名成 map）比“缺 map 表”更具体，
		// 不该因为 schema 恰好写在前面就被盖掉。
		for _, p := range pairs {
			if p.key == "spec" {
				return nil, errSpecRenamed
			}
		}
		for _, p := range pairs {
			if p.key == "schema" || p.key == "name" {
				return nil, fmt.Errorf("出现了外层字段 %q 却没有 map 表；"+
					"完整写法需要 \"map\": {...}（设备侧字段确实叫 %q 时，请用完整写法把它写进 map 表里）",
					p.key, p.key)
			}
		}
		return mapToFields(pairs, m)
	}

	seenTable := false
	for _, p := range pairs {
		switch p.key {
		case "schema":
			if err := json.Unmarshal(p.val, &m.Schema); err != nil {
				return nil, fmt.Errorf("schema: %w", err)
			}
			switch m.Schema {
			case "", SchemaModulePath, SchemaV1:
			default:
				return nil, fmt.Errorf("schema 取值 %q 无法识别（可填 %q、%q 或留空）",
					m.Schema, SchemaModulePath, SchemaV1)
			}
		case "name":
			if err := json.Unmarshal(p.val, &m.Name); err != nil {
				return nil, fmt.Errorf("name: %w", err)
			}
		case "spec":
			return nil, errSpecRenamed
		case "map":
			if seenTable {
				return nil, fmt.Errorf("map 只能写一个")
			}
			seenTable = true
			t, err := decodeOrderedObject(bytes.TrimSpace(p.val))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p.key, err)
			}
			table = t
		default:
			return nil, fmt.Errorf("未知的顶层字段 %q", p.key)
		}
	}
	return mapToFields(table, m)
}

// mapToFields 把 map 表的每一项编译成一条 Field。
func mapToFields(table []rawPair, m *Mapping) (*Mapping, error) {
	for _, p := range table {
		raw := bytes.TrimSpace(p.val)
		if len(raw) == 0 {
			return nil, fmt.Errorf("map[%q] 的值为空", p.key)
		}
		if raw[0] == '"' {
			// 简写：直接给平台侧路径
			var platform string
			if err := json.Unmarshal(raw, &platform); err != nil {
				return nil, fmt.Errorf("map[%q]: %w", p.key, err)
			}
			m.Fields = append(m.Fields, Field{A: platform, B: p.key})
			continue
		}
		if raw[0] != '{' {
			return nil, fmt.Errorf("map[%q] 的值必须是平台侧路径字符串或带选项的对象", p.key)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		var e mapEntry
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("map[%q]: %w", p.key, err)
		}
		field := Field{
			A:         e.Key,
			B:         p.key,
			Value:     e.Value,
			Reverse:   e.Reverse,
			Convert:   e.Convert,
			Enum:      e.Enum,
			Required:  e.Required,
			Only:      e.Only,
			OmitEmpty: e.OmitEmpty,
			Comment:   e.Comment,
		}
		// 写了就算写了，即使写的是 null。
		if e.Const != nil {
			if err := json.Unmarshal(e.Const, &field.Const); err != nil {
				return nil, fmt.Errorf("map[%q]: const: %w", p.key, err)
			}
			field.HasConst = true
		}
		if e.Default != nil {
			if err := json.Unmarshal(e.Default, &field.Default); err != nil {
				return nil, fmt.Errorf("map[%q]: default: %w", p.key, err)
			}
			field.HasDefault = true
		}
		m.Fields = append(m.Fields, field)
	}
	return m, nil
}

// resolveConverter 先查映射私有转换器，再查全局注册表与转换器族；
// 两者都支持 ReversePrefix 前缀。
func (m *Mapping) resolveConverter(name string) (Converter, error) {
	if c, ok := m.Converters[name]; ok {
		if c.Name == "" {
			c.Name = name
		}
		return c, nil
	}
	if base, ok := strings.CutPrefix(name, ReversePrefix); ok && base != "" {
		if c, ok := m.Converters[base]; ok {
			return reversed(c, name), nil
		}
	}
	return lookupDetailed(name)
}

// Validate 检查映射是否自洽：路径语法、单边规则、转换器与枚举表。
// 后台保存映射配置时应先调用它，把配置错误挡在下发之前。
func (m *Mapping) Validate() error {
	_, err := compile(m)
	return err
}
