package jsonshift

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Converter 是一对可逆的值转换函数。Forward 用于 a -> b，Reverse 用于 b -> a。
// 命名约定：名字描述 A 侧到 B 侧的变化，如 "string2int" 表示 A 侧是字符串、B 侧是数字。
type Converter struct {
	Name    string
	Forward func(any) (any, error)
	Reverse func(any) (any, error)
}

func (c Converter) apply(v any, dir Direction) (any, error) {
	fn := c.Forward
	if dir == BToA {
		fn = c.Reverse
	}
	if fn == nil {
		return nil, fmt.Errorf("转换器 %q 不支持 %s 方向", c.Name, dir)
	}
	return fn(v)
}

// ConverterFactory 按参数生产转换器，支持 "scale:0.1" 这类带参写法。
type ConverterFactory func(arg string) (Converter, error)

// ReversePrefix 加在转换器名前面表示交换正反方向。内置转换器的名字都按
// “a 侧 2 b 侧”命名，实际配置里方向反了就加这个前缀，例如设备把数值塞在
// 字符串里上报时用 "~string2int"。对带参转换器与自定义转换器同样有效。
const ReversePrefix = "~"

// reversed 返回交换了正反方向的转换器副本。
func reversed(c Converter, name string) Converter {
	return Converter{Name: name, Forward: c.Reverse, Reverse: c.Forward}
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Converter{}
	factories  = map[string]ConverterFactory{}
)

// Register 注册（或覆盖）一个全局转换器，可在 init 中调用。
func Register(c Converter) {
	if c.Name == "" {
		panic("jsonshift: 转换器必须有名字")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[c.Name] = c
}

// RegisterFactory 注册一个带参转换器族，用法形如 "name:arg"。
func RegisterFactory(name string, f ConverterFactory) {
	if name == "" || f == nil {
		panic("jsonshift: 转换器族必须有名字和构造函数")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	factories[name] = f
}

// lookupDetailed 与 Lookup 相同，但会区分“名字不存在”与“带参转换器的参数非法”，
// 便于后台保存配置时给出可操作的报错。
func lookupDetailed(name string) (Converter, error) {
	if base, ok := strings.CutPrefix(name, ReversePrefix); ok {
		if base == "" {
			return Converter{}, fmt.Errorf("%q 缺少转换器名", name)
		}
		c, err := lookupDetailed(base)
		if err != nil {
			return Converter{}, err
		}
		return reversed(c, name), nil
	}
	registryMu.RLock()
	if c, ok := registry[name]; ok {
		registryMu.RUnlock()
		return c, nil
	}
	base, arg, hasArg := strings.Cut(name, ":")
	f, ok := factories[base]
	registryMu.RUnlock()
	if !hasArg || !ok {
		return Converter{}, fmt.Errorf("未注册的转换器 %q", name)
	}
	c, err := f(arg)
	if err != nil {
		return Converter{}, err
	}
	c.Name = name
	return c, nil
}

// Lookup 按名字查找全局转换器；名字含 ":" 时交给对应的转换器族构造，
// 带 ReversePrefix 前缀时返回方向交换后的副本。
func Lookup(name string) (Converter, bool) {
	c, err := lookupDetailed(name)
	return c, err == nil
}

// Converters 返回全部已注册的全局转换器名字；带参的转换器族以 "name:" 结尾。
func Converters() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry)+len(factories))
	for n := range registry {
		names = append(names, n)
	}
	for n := range factories {
		names = append(names, n+":")
	}
	sort.Strings(names)
	return names
}

// toFloat 把 JSON 数字（float64 / json.Number / 原生整型）统一成 float64。
func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	default:
		return 0, fmt.Errorf("期望数字，得到 %T", v)
	}
}

func toString(v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("期望字符串，得到 %T", v)
	}
	return s, nil
}

func formatNumber(f float64) string {
	if math.Abs(f) < 1<<53 && f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// csvField 把数组元素渲染成一个 CSV 字段。只接受标量：对象/数组没有无歧义的
// CSV 表示，含逗号的字符串拼回去会被下次切分切错，都直接报错而不是硬拼——
// 之前用 %v 会把 null 写成字面量 "<nil>"、把对象写成 Go 的 map[k:1]。
// null 渲染成空字段，这是 CSV 的通行约定。
func csvField(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		if strings.ContainsRune(t, ',') {
			return "", fmt.Errorf("字符串 %q 含逗号，拼成 CSV 后无法再切回来", t)
		}
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	default:
		f, err := toFloat(v)
		if err != nil {
			return "", fmt.Errorf("csv2array 只支持标量元素（字符串/数字/布尔/null），得到 %T", v)
		}
		return formatNumber(f), nil
	}
}

func init() {
	Register(Converter{
		Name: "string2int",
		Forward: func(v any) (any, error) {
			s, err := toString(v)
			if err != nil {
				return nil, err
			}
			n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%q 不是整数", s)
			}
			return n, nil
		},
		Reverse: func(v any) (any, error) {
			f, err := toFloat(v)
			if err != nil {
				return nil, err
			}
			return formatNumber(f), nil
		},
	})

	Register(Converter{
		Name: "string2float",
		Forward: func(v any) (any, error) {
			s, err := toString(v)
			if err != nil {
				return nil, err
			}
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return nil, fmt.Errorf("%q 不是浮点数", s)
			}
			return f, nil
		},
		Reverse: func(v any) (any, error) {
			f, err := toFloat(v)
			if err != nil {
				return nil, err
			}
			return formatNumber(f), nil
		},
	})

	Register(Converter{
		Name: "string2bool",
		Forward: func(v any) (any, error) {
			s, err := toString(v)
			if err != nil {
				return nil, err
			}
			b, err := strconv.ParseBool(strings.TrimSpace(s))
			if err != nil {
				return nil, fmt.Errorf("%q 不是布尔值", s)
			}
			return b, nil
		},
		Reverse: func(v any) (any, error) {
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("期望布尔值，得到 %T", v)
			}
			return strconv.FormatBool(b), nil
		},
	})

	Register(Converter{
		Name: "int2bool",
		Forward: func(v any) (any, error) {
			f, err := toFloat(v)
			if err != nil {
				return nil, err
			}
			return f != 0, nil
		},
		Reverse: func(v any) (any, error) {
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("期望布尔值，得到 %T", v)
			}
			if b {
				return int64(1), nil
			}
			return int64(0), nil
		},
	})

	Register(Converter{
		Name: "lower2upper",
		Forward: func(v any) (any, error) {
			s, err := toString(v)
			if err != nil {
				return nil, err
			}
			return strings.ToUpper(s), nil
		},
		Reverse: func(v any) (any, error) {
			s, err := toString(v)
			if err != nil {
				return nil, err
			}
			return strings.ToLower(s), nil
		},
	})

	Register(Converter{
		Name: "unix2rfc3339",
		Forward: func(v any) (any, error) {
			f, err := toFloat(v)
			if err != nil {
				return nil, err
			}
			return time.Unix(int64(f), 0).UTC().Format(time.RFC3339), nil
		},
		Reverse: func(v any) (any, error) {
			s, err := toString(v)
			if err != nil {
				return nil, err
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return nil, fmt.Errorf("%q 不是 RFC3339 时间", s)
			}
			return t.Unix(), nil
		},
	})

	Register(Converter{
		Name: "millis2rfc3339",
		Forward: func(v any) (any, error) {
			f, err := toFloat(v)
			if err != nil {
				return nil, err
			}
			return time.UnixMilli(int64(f)).UTC().Format(time.RFC3339Nano), nil
		},
		Reverse: func(v any) (any, error) {
			s, err := toString(v)
			if err != nil {
				return nil, err
			}
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return nil, fmt.Errorf("%q 不是 RFC3339 时间", s)
			}
			return t.UnixMilli(), nil
		},
	})

	Register(Converter{
		Name: "csv2array",
		Forward: func(v any) (any, error) {
			s, err := toString(v)
			if err != nil {
				return nil, err
			}
			if s == "" {
				return []any{}, nil
			}
			parts := strings.Split(s, ",")
			out := make([]any, len(parts))
			for i, p := range parts {
				out[i] = strings.TrimSpace(p)
			}
			return out, nil
		},
		Reverse: func(v any) (any, error) {
			arr, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("期望数组，得到 %T", v)
			}
			parts := make([]string, len(arr))
			for i, e := range arr {
				s, err := csvField(e)
				if err != nil {
					return nil, fmt.Errorf("第 %d 个元素: %w", i, err)
				}
				parts[i] = s
			}
			return strings.Join(parts, ","), nil
		},
	})

	// json2object：A 侧是内嵌的 JSON 字符串，B 侧是展开后的对象/数组。
	Register(Converter{
		Name: "json2object",
		Forward: func(v any) (any, error) {
			s, err := toString(v)
			if err != nil {
				return nil, err
			}
			var out any
			if err := json.Unmarshal([]byte(s), &out); err != nil {
				return nil, fmt.Errorf("内嵌 JSON 解析失败: %w", err)
			}
			return out, nil
		},
		Reverse: func(v any) (any, error) {
			b, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			return string(b), nil
		},
	})
}

// normalizeNumber 规整浮点运算结果：先按 float64 的有效位消掉
// 0.30000000000000004 这类尾差，是整数就按整数输出。
func normalizeNumber(r float64) any {
	if rounded, err := strconv.ParseFloat(strconv.FormatFloat(r, 'g', 15, 64), 64); err == nil {
		r = rounded
	}
	// 先判范围再转 int64：超出 int64 的浮点转整型在 Go 里是实现定义行为，
	// 不该让正确性依赖它。
	if math.Abs(r) < 1<<53 && r == float64(int64(r)) {
		return int64(r)
	}
	return r
}
