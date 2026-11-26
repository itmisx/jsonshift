// Package jsonshift 按一份声明式的映射关系，在两种 JSON 结构之间双向转换。
//
// 它为 IoT 物模型的属性转换而写：A 侧是平台标准格式，B 侧是厂商设备格式，
// 后台只配一份映射，收到设备上报时走 Uplink、下发给设备时走 Downlink。
//
// 配置是一张 `"设备侧路径": "平台侧路径"` 的平铺表，从左到右正好是上行的流向：
//
//	m, _ := jsonshift.ParseMapping([]byte(`{
//	  "dev_sn":  "deviceId",
//	  "ch[*].t": "properties.channels[*].temperature"
//	}`))
//
//	s, _ := jsonshift.New(m)      // 预编译一次，按产品型号缓存
//	std, _ := s.Uplink(devJSON)   // 设备上报 -> 平台标准格式
//	dev, _ := s.Downlink(stdJSON) // 平台标准格式 -> 设备格式，再下发
//
// 解析与编译一步到位时用 Compile；只校验配置（后台保存那一步）用 ParseMapping。
// 需要缩放、枚举一类的值变换时，表里的值改写成带选项的对象（见 ParseMapping）。
// Shifter 创建后只读，可被多个 goroutine 并发使用。
package jsonshift

import (
	"encoding/json"
	"fmt"
)

// Transform 按映射关系把一段 JSON 转换到另一侧，dir 决定方向。
// 这是本库的主入口：输入 JSON 字节 + 映射关系，输出 JSON 字节。
// 每次调用都会预编译一遍映射；在热路径上请改用 New + Shifter。
func Transform(data []byte, m *Mapping, dir Direction) ([]byte, error) {
	s, err := New(m)
	if err != nil {
		return nil, err
	}
	return s.Transform(data, dir)
}

// TransformValue 在已解码的值（map[string]any / []any）上做同样的转换，
// 适合与上游解码结果串联，省掉一次编解码。
func TransformValue(src any, m *Mapping, dir Direction) (any, error) {
	s, err := New(m)
	if err != nil {
		return nil, err
	}
	return s.TransformValue(src, dir)
}

// Compile 解析后台配置并直接编译成可复用的 Shifter，映射只编译一遍。
// 等价于 ParseMapping + New，但省掉中间那次校验编译；网关启动时按产品型号
// 构建缓存用它最合适。只需要校验配置（不需要转换器）时用 ParseMapping。
func Compile(data []byte) (*Shifter, error) {
	m, err := parseMapping(data)
	if err != nil {
		return nil, err
	}
	return New(m)
}

// Shifter 是预编译好的转换器：路径已切段、转换器与枚举表已查妥。
// 创建后只读，可安全地被多个 goroutine 并发调用，适合常驻网关按产品型号缓存。
type Shifter struct {
	m *Mapping
	c *compiled
}

// New 校验并预编译映射，返回可复用的 Shifter。
func New(m *Mapping) (*Shifter, error) {
	c, err := compile(m)
	if err != nil {
		return nil, err
	}
	return &Shifter{m: m, c: c}, nil
}

// Transform 按指定方向转换 JSON 字节。
func (s *Shifter) Transform(data []byte, dir Direction) ([]byte, error) {
	var src any
	if err := json.Unmarshal(data, &src); err != nil {
		return nil, fmt.Errorf("jsonshift: 输入不是合法 JSON: %w", err)
	}
	out, err := s.TransformValue(src, dir)
	if err != nil {
		return nil, err
	}
	res, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("jsonshift: 序列化结果失败: %w", err)
	}
	return res, nil
}

// TransformValue 在已解码的值上按指定方向转换。
func (s *Shifter) TransformValue(src any, dir Direction) (any, error) {
	var out any
	for i := range s.c.fields {
		cf := &s.c.fields[i]
		if !cf.f.applies(dir) {
			continue
		}
		srcSegs, dstSegs := cf.aSegs, cf.bSegs
		if dir == BToA {
			srcSegs, dstSegs = cf.bSegs, cf.aSegs
		}
		if len(dstSegs) == 0 {
			continue // 该方向没有目标路径，等价于丢弃这个字段
		}
		var err error
		out, err = applyField(out, src, cf, srcSegs, dstSegs, dir)
		if err != nil {
			return nil, fmt.Errorf("jsonshift: map[%q] (%s) %s: %w",
				cf.f.key(), cf.f.route(dir), dir, err)
		}
	}
	return out, nil
}

// AToB 把平台标准格式转成厂商设备格式。
func (s *Shifter) AToB(data []byte) ([]byte, error) { return s.Transform(data, AToB) }

// BToA 把厂商设备格式转成平台标准格式。
func (s *Shifter) BToA(data []byte) ([]byte, error) { return s.Transform(data, BToA) }

// Uplink 是 BToA 的别名：收到设备上报后，把厂商设备格式转回平台标准格式。
func (s *Shifter) Uplink(data []byte) ([]byte, error) { return s.Transform(data, BToA) }

// Downlink 是 AToB 的别名：下发给设备前，把平台标准格式转成厂商设备格式。
func (s *Shifter) Downlink(data []byte) ([]byte, error) { return s.Transform(data, AToB) }

// Mapping 返回底层映射。
func (s *Shifter) Mapping() *Mapping { return s.m }

// applyField 处理单条规则。两侧的 [*] 自左向右成对展开；成对展开完后若还有
// 一侧剩下 [*]，就退化成聚集（源端 [*] 收成数组）或散开（源端数组铺到目标端 [*]）。
func applyField(out, src any, cf *compiledField, srcSegs, dstSegs []segment, dir Direction) (any, error) {
	si, di := wildcardIndex(srcSegs), wildcardIndex(dstSegs)
	if si < 0 || di < 0 {
		return applyLeaf(out, src, cf, srcSegs, dstSegs, di, dir)
	}

	arr, found := getPath(src, srcSegs[:si])
	if !found || arr == nil {
		if cf.f.Required {
			return nil, fmt.Errorf("缺少必填数组 %s", pathString(srcSegs[:si]))
		}
		return out, nil
	}
	list, ok := arr.([]any)
	if !ok {
		return nil, fmt.Errorf("%s 期望数组，得到 %T", pathString(srcSegs[:si]), arr)
	}
	if len(list) == 0 {
		return materializeEmpty(out, dstSegs[:di])
	}

	var err error
	for k := range list {
		out, err = applyField(out, src, cf, withIndex(srcSegs, si, k), withIndex(dstSegs, di, k), dir)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// applyLeaf 完成一次“读源端 -> 值变换 -> 写目标端”，di >= 0 时按散开写入。
func applyLeaf(out, src any, cf *compiledField, srcSegs, dstSegs []segment, di int, dir Direction) (any, error) {
	f := cf.f
	var (
		val     any
		found   bool
		literal bool // 常量/默认值不再经过转换器
	)
	switch {
	case f.hasConst():
		val, found, literal = f.Const, true, true
	case len(srcSegs) > 0:
		var err error
		val, found, err = resolveValue(src, srcSegs)
		if err != nil {
			return nil, err
		}
	}

	if !found {
		switch {
		case f.hasDefault():
			val, literal = f.Default, true
		case f.Required:
			return nil, fmt.Errorf("缺少必填字段 %s", pathString(srcSegs))
		default:
			return out, nil // 设备上报的属性是子集，缺失即跳过
		}
	}

	if !literal && cf.hasValueMap() && val != nil {
		// 聚集/散开时值是嵌套数组，变换要作用在叶子上，深度即两侧通配符数之差。
		depth := wildcardCount(srcSegs)
		if di >= 0 {
			depth = wildcardCount(dstSegs)
		}
		converted, err := mapAtDepth(val, depth, func(leaf any) (any, error) { return cf.mapLeaf(leaf, src, dir) })
		if err != nil {
			return nil, err
		}
		val = converted
	}

	if f.OmitEmpty && isEmpty(val) {
		return out, nil
	}
	if di >= 0 {
		return scatter(out, val, dstSegs, di)
	}
	return setPath(out, dstSegs, val)
}

// resolveValue 读取源端路径的值；遇到 [*] 就把该层收成数组（可嵌套）。
func resolveValue(src any, segs []segment) (any, bool, error) {
	i := wildcardIndex(segs)
	if i < 0 {
		v, ok := getPath(src, segs)
		return v, ok, nil
	}
	arr, found := getPath(src, segs[:i])
	if !found || arr == nil {
		return nil, false, nil
	}
	list, ok := arr.([]any)
	if !ok {
		return nil, false, fmt.Errorf("%s 期望数组，得到 %T", pathString(segs[:i]), arr)
	}
	out := make([]any, len(list))
	for k := range list {
		v, ok, err := resolveValue(src, withIndex(segs, i, k))
		if err != nil {
			return nil, false, err
		}
		if !ok {
			v = nil // 缺失的元素留空位，保持下标对齐
		}
		out[k] = v
	}
	return out, true, nil
}

// scatter 把数组 val 铺到目标端的 [*] 上（可嵌套），是 resolveValue 的逆操作。
func scatter(out, val any, dstSegs []segment, di int) (any, error) {
	list, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("%s 需要数组才能展开到 [*]，得到 %T", pathString(dstSegs), val)
	}
	if len(list) == 0 {
		return materializeEmpty(out, dstSegs[:di])
	}
	var err error
	for k, elem := range list {
		next := withIndex(dstSegs, di, k)
		if j := wildcardIndex(next); j >= 0 {
			out, err = scatter(out, elem, next, j)
		} else {
			out, err = setPath(out, next, elem)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// materializeEmpty 在目标端物化一个空数组（该位置已有内容时保持不动）。
func materializeEmpty(out any, prefix []segment) (any, error) {
	// 注意 exists 不足以判断：prefix 为空（顶层就是数组）时 getPath 恒为
	// (out, true)，out 还是 nil 也一样，所以要再看值本身是不是真的有内容。
	if v, exists := getPath(out, prefix); exists && v != nil {
		return out, nil
	}
	return setPath(out, prefix, []any{})
}

// mapAtDepth 对嵌套 depth 层的数组逐叶子应用 fn；depth 为 0 时直接作用于值本身。
func mapAtDepth(v any, depth int, fn func(any) (any, error)) (any, error) {
	if depth <= 0 {
		return fn(v)
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("期望第 %d 层是数组，得到 %T", depth, v)
	}
	out := make([]any, len(list))
	for i, elem := range list {
		if elem == nil {
			continue
		}
		r, err := mapAtDepth(elem, depth-1, fn)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}
