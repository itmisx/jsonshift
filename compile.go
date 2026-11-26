package jsonshift

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// compiledField 是一条规则预解析后的形态：路径已切好段，转换器/枚举表/表达式
// 都已就绪，转换时不再做任何解析工作。
type compiledField struct {
	idx   int
	f     *Field
	aSegs []segment
	bSegs []segment

	conv *Converter     // convert 指定的可逆转换器
	fwd  map[string]any // enum 的 a->b 查表，键是值的规范 JSON 编码
	rev  map[string]any // enum 的 b->a 查表

	exprUp   exprNode // 上行表达式：平台值 = f(设备值)
	exprDown exprNode // 下行表达式；未显式给出时由 exprUp 自动求逆
	upBool   bool     // 上行结果是否按 true/false 输出
	downBool bool
}

// compiled 是整份映射的预编译结果，只读，可被多个 goroutine 并发使用。
type compiled struct {
	fields []compiledField
}

func compile(m *Mapping) (*compiled, error) {
	if m == nil {
		return nil, fmt.Errorf("jsonshift: 映射为空")
	}
	if len(m.Fields) == 0 {
		return nil, fmt.Errorf("jsonshift: 映射未定义任何字段")
	}
	c := &compiled{fields: make([]compiledField, 0, len(m.Fields))}
	for i := range m.Fields {
		f := &m.Fields[i]
		cf, err := compileField(i, f, m)
		if err != nil {
			return nil, fmt.Errorf("jsonshift: map[%q]: %w", f.key(), err)
		}
		c.fields = append(c.fields, cf)
	}
	return c, nil
}

func compileField(idx int, f *Field, m *Mapping) (compiledField, error) {
	cf := compiledField{idx: idx, f: f}

	if f.A == "" && f.B == "" {
		return cf, fmt.Errorf("平台侧与设备侧路径不能同时为空")
	}
	var err error
	if cf.aSegs, err = parsePath(f.A); err != nil {
		return cf, err
	}
	if cf.bSegs, err = parsePath(f.B); err != nil {
		return cf, err
	}
	if len(cf.aSegs) == 0 || len(cf.bSegs) == 0 {
		// 单边规则只能用于写常量/默认值，否则另一个方向无源可读。
		if !f.hasConst() && !f.hasDefault() {
			return cf, fmt.Errorf("单边规则（只有一侧路径）必须提供 const 或 default")
		}
	}

	switch f.Only {
	case "", ScopeBoth, ScopeAToB, ScopeBToA:
	default:
		return cf, fmt.Errorf("only 取值非法: %q（可选 both/a2b/b2a）", f.Only)
	}

	mechanisms := 0
	for _, used := range []bool{f.Convert != "", len(f.Enum) > 0, f.Value != "" || f.Reverse != ""} {
		if used {
			mechanisms++
		}
	}
	if mechanisms > 1 {
		return cf, fmt.Errorf("value/reverse、convert、enum 三者只能用一个")
	}
	// const 不读源端，值变换永远没有输入可做——这种组合是死配置，直接拒绝，
	// 而不是悄悄把 value/enum/convert 丢掉。
	if f.hasConst() && mechanisms > 0 {
		return cf, fmt.Errorf("const 直接写常量、不读源端，不能再配 value/reverse、convert 或 enum")
	}

	if err := cf.compileExprs(f); err != nil {
		return cf, err
	}

	if f.Convert != "" {
		conv, err := m.resolveConverter(f.Convert)
		if err != nil {
			return cf, err
		}
		// 单向转换器配在双向规则上，要在保存配置时就暴露，而不是等到某次转换才报。
		if conv.Forward == nil && f.Only != ScopeBToA {
			return cf, fmt.Errorf("转换器 %q 没有正向（a->b）实现", f.Convert)
		}
		if conv.Reverse == nil && f.Only != ScopeAToB {
			return cf, fmt.Errorf("转换器 %q 没有反向（b->a）实现", f.Convert)
		}
		cf.conv = &conv
	}
	if len(f.Enum) > 0 {
		if cf.fwd, cf.rev, err = buildEnum(f.Enum); err != nil {
			return cf, err
		}
	}
	// 两个方向的表达式都做一次静态检查：字面量除以 0 要在保存配置时就报出来。
	for _, pair := range [][2]any{{"value", cf.exprUp}, {"reverse", cf.exprDown}} {
		if n, _ := pair[1].(exprNode); n != nil {
			if err := checkDivZero(n); err != nil {
				return cf, fmt.Errorf("%s 表达式 %q: %w", pair[0], exprString(n), err)
			}
		}
	}

	// 含通配符的规则无法用绝对路径引用“当前元素”，所以只允许自引用。
	if cf.hasExpr() && (wildcardCount(cf.aSegs) > 0 || wildcardCount(cf.bSegs) > 0) {
		for _, n := range []exprNode{cf.exprUp, cf.exprDown} {
			if n != nil && !allRefsSelf(n) {
				return cf, fmt.Errorf("含 [*] 的规则里，表达式只能引用 {.}（当前元素的值）")
			}
		}
	}
	// 表达式里所有函数都在这一步解析完，求值路径不再查注册表。
	for _, n := range []exprNode{cf.exprUp, cf.exprDown} {
		if n != nil {
			if err := resolveFuncs(n); err != nil {
				return cf, err
			}
		}
	}
	return cf, nil
}

// buildEnum 把 [[平台侧值, 设备侧值], ...] 编译成双向查表，并要求两侧都无重复值
// （否则反向转换无法确定唯一结果）。
func buildEnum(pairs [][2]any) (fwd, rev map[string]any, err error) {
	fwd = make(map[string]any, len(pairs))
	rev = make(map[string]any, len(pairs))
	for i, p := range pairs {
		ak, err := enumKey(p[0])
		if err != nil {
			return nil, nil, fmt.Errorf("enum[%d] 的平台侧值无法编码: %w", i, err)
		}
		bk, err := enumKey(p[1])
		if err != nil {
			return nil, nil, fmt.Errorf("enum[%d] 的设备侧值无法编码: %w", i, err)
		}
		if _, dup := fwd[ak]; dup {
			return nil, nil, fmt.Errorf("enum 中平台侧值 %v 重复，上行结果将不唯一", p[0])
		}
		if _, dup := rev[bk]; dup {
			return nil, nil, fmt.Errorf("enum 中设备侧值 %v 重复，下行结果将不唯一", p[1])
		}
		fwd[ak] = p[1]
		rev[bk] = p[0]
	}
	return fwd, rev, nil
}

// enumKey 用值的规范 JSON 编码作为查表键，这样数字、字符串、布尔都能当枚举值。
func enumKey(v any) (string, error) {
	switch n := v.(type) {
	case int:
		v = float64(n)
	case int8:
		v = float64(n)
	case int16:
		v = float64(n)
	case int32:
		v = float64(n)
	case int64:
		v = float64(n)
	case uint:
		v = float64(n)
	case uint8:
		v = float64(n)
	case uint16:
		v = float64(n)
	case uint32:
		v = float64(n)
	case uint64:
		v = float64(n)
	case float32:
		// 直接 float64(float32(1.1)) 会得到 1.100000023841858，与 JSON 里的
		// 1.1 对不上。先按 32 位的最短十进制表示规整，再放宽到 float64。
		f, err := strconv.ParseFloat(strconv.FormatFloat(float64(n), 'g', -1, 32), 64)
		if err != nil {
			return "", err
		}
		v = f
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return "", err
		}
		v = f
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// hasValueMap 表示这条规则需要做值层面的变换。
func (cf *compiledField) hasValueMap() bool {
	return cf.conv != nil || cf.fwd != nil || cf.hasExpr()
}

// mapLeaf 对单个叶子值做转换器/枚举/表达式变换。
func (cf *compiledField) mapLeaf(v any, src any, dir Direction) (any, error) {
	switch {
	case cf.hasExpr():
		return cf.evalLeaf(v, src, dir)
	case cf.conv != nil:
		out, err := cf.conv.apply(v, dir)
		if err != nil {
			return nil, fmt.Errorf("转换器 %q 失败: %w", cf.conv.Name, err)
		}
		return out, nil
	}
	table := cf.fwd
	if dir == BToA {
		table = cf.rev
	}
	k, err := enumKey(v)
	if err != nil {
		return nil, err
	}
	mapped, ok := table[k]
	if !ok {
		return nil, fmt.Errorf("枚举表里没有值 %v（%T）", v, v)
	}
	return mapped, nil
}

// ---------- 表达式 ----------

// compileExprs 编译上/下行表达式，并在需要时自动求逆。
func (cf *compiledField) compileExprs(f *Field) error {
	if f.Value == "" && f.Reverse == "" {
		return nil
	}

	// 上行读设备侧（b），下行读平台侧（a）：各自的"自引用"就是各自的源路径。
	if f.Value != "" {
		n, err := parseExpr(f.Value)
		if err != nil {
			return fmt.Errorf("value: %w", err)
		}
		if err := markSelfRefs(n, f.B); err != nil {
			return fmt.Errorf("value: %w", err)
		}
		cf.exprUp = n
		cf.upBool = isBoolResult(n)
	}
	if f.Reverse != "" {
		n, err := parseExpr(f.Reverse)
		if err != nil {
			return fmt.Errorf("reverse: %w", err)
		}
		if err := markSelfRefs(n, f.A); err != nil {
			return fmt.Errorf("reverse: %w", err)
		}
		cf.exprDown = n
		cf.downBool = isBoolResult(n)
	}

	// 单向规则只跑一个方向，写错那一边会被整条忽略（或被悄悄求逆成相反的
	// 含义），所以这里要求 only 与表达式的方向一致，而不是替用户猜。
	switch f.Only {
	case ScopeBToA: // 只上行，用的是 value
		if cf.exprUp == nil {
			return fmt.Errorf(`only "b2a" 只走上行，上行表达式请写在 value 里（reverse 不会生效）`)
		}
		if f.Reverse != "" {
			return fmt.Errorf(`only "b2a" 只走上行，reverse 不会生效；请删掉 reverse 或去掉 only`)
		}
		return nil
	case ScopeAToB: // 只下行，用的是 reverse
		if cf.exprDown == nil {
			return fmt.Errorf(`only "a2b" 只走下行，下行表达式请写在 reverse 里；` +
				`value 写的是上行式（平台值 = f(设备值)），在这条规则上不会生效`)
		}
		if f.Value != "" {
			return fmt.Errorf(`only "a2b" 只走下行，value 不会生效；请删掉 value 或去掉 only`)
		}
		return nil
	}

	// 双向规则：缺的那一边尝试自动求逆。
	if cf.exprDown == nil {
		inv, err := autoInvert(cf.exprUp, "value")
		if err != nil {
			return err
		}
		cf.exprDown = inv
		cf.downBool = false
	}
	if cf.exprUp == nil {
		inv, err := autoInvert(cf.exprDown, "reverse")
		if err != nil {
			return err
		}
		cf.exprUp = inv
		cf.upBool = false
	}
	return nil
}

// autoInvert 对只含一次自引用的表达式求逆。其余情形拒绝，并说明补救办法。
func autoInvert(n exprNode, which string) (exprNode, error) {
	if n == nil {
		return nil, fmt.Errorf("%s 为空，无法求逆", which)
	}
	const remedy = "；请显式提供另一个方向的表达式，或用 only 限定成单向规则"
	switch refs := countRefs(n); {
	case refs == 0:
		return nil, fmt.Errorf("%s %q 不含字段引用，无法求逆；要写常量请用 const", which, exprString(n))
	case refs > 1:
		return nil, fmt.Errorf("%s %q 含 %d 处字段引用，无法自动求逆"+remedy, which, exprString(n), refs)
	}
	if !allRefsSelf(n) {
		return nil, fmt.Errorf("%s %q 引用了别的字段，反方向读不到它"+remedy, which, exprString(n))
	}
	inv, err := invertExpr(n, &inNode{})
	if err != nil {
		return nil, fmt.Errorf("%s %q 无法自动求逆: %w"+remedy, which, exprString(n), err)
	}
	return inv, nil
}

// markSelfRefs 把 {.} 和指向本字段源路径的引用标成自引用，其余按绝对路径解析。
func markSelfRefs(n exprNode, ownPath string) error {
	switch t := n.(type) {
	case *refNode:
		if t.raw == "." || t.raw == ownPath {
			t.self = true
			t.segs = nil
			return nil
		}
		if len(t.segs) == 0 {
			return fmt.Errorf("引用 {%s} 不是合法路径", t.raw)
		}
		// 绝对路径引用是按整条路径取单个值的，[*] 在这里没有意义：取值必然
		// 失败。与其每次转换都报“在源数据里不存在”，不如在保存配置时就挡住。
		if wildcardCount(t.segs) > 0 {
			return fmt.Errorf("引用 {%s} 含 [*]；跨字段引用只能是指向单个值的确定路径，"+
				"要引用当前元素请用 {.}", t.raw)
		}
	case *unaryNode:
		return markSelfRefs(t.x, ownPath)
	case *binNode:
		if err := markSelfRefs(t.l, ownPath); err != nil {
			return err
		}
		return markSelfRefs(t.r, ownPath)
	case *condNode:
		for _, x := range []exprNode{t.c, t.t, t.f} {
			if err := markSelfRefs(x, ownPath); err != nil {
				return err
			}
		}
	case *callNode:
		for _, a := range t.args {
			if err := markSelfRefs(a, ownPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// allRefsSelf 判断表达式里每一处字段引用都是自引用。
func allRefsSelf(n exprNode) bool {
	switch t := n.(type) {
	case *refNode:
		return t.self
	case *unaryNode:
		return allRefsSelf(t.x)
	case *binNode:
		return allRefsSelf(t.l) && allRefsSelf(t.r)
	case *condNode:
		return allRefsSelf(t.c) && allRefsSelf(t.t) && allRefsSelf(t.f)
	case *callNode:
		for _, a := range t.args {
			if !allRefsSelf(a) {
				return false
			}
		}
	}
	return true
}

// hasExpr 表示这条规则用表达式做值变换。
func (cf *compiledField) hasExpr() bool { return cf.exprUp != nil || cf.exprDown != nil }

// evalLeaf 用表达式算一个叶子值。
func (cf *compiledField) evalLeaf(v any, src any, dir Direction) (any, error) {
	n, wantBool := cf.exprDown, cf.downBool
	if dir == BToA {
		n, wantBool = cf.exprUp, cf.upBool
	}
	if n == nil {
		return nil, fmt.Errorf("本规则没有 %s 方向的表达式", dir)
	}
	f, err := evalExpr(n, &evalCtx{self: v, src: src})
	if err != nil {
		return nil, err
	}
	if wantBool {
		return f != 0, nil
	}
	// NaN / ±Inf 不是合法 JSON 数值。放过去的话 Transform 会在最后序列化时
	// 以一句不带规则定位的错误失败，而 TransformValue 更糟——它会把一个无法
	// 序列化的值静默交给调用方。在这里就地报错，带上表达式和输入值。
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("表达式 %q 在输入 %v 上得到 %s，不是合法的 JSON 数值"+
			"（检查定义域、量纲与溢出）", exprString(n), v, formatSpecial(f))
	}
	return normalizeNumber(f), nil
}

func formatSpecial(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "+Inf"
	default:
		return "-Inf"
	}
}

// DerivedReverse 返回自动求出的下行表达式文本，便于在后台把推导结果显示给配置者。
// 该规则没用表达式、或下行表达式是显式写的，返回空串。
func (s *Shifter) DerivedReverse(deviceKey string) string {
	for i := range s.c.fields {
		cf := &s.c.fields[i]
		if cf.f.B == deviceKey && cf.f.Reverse == "" && cf.exprDown != nil {
			return exprString(cf.exprDown)
		}
	}
	return ""
}
