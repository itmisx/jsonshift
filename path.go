package jsonshift

import (
	"fmt"
	"strconv"
	"strings"
)

// segKind 描述一个路径段的类型。
type segKind int

const (
	segKey      segKind = iota // 对象字段，如 user
	segIndex                   // 数组下标，如 [0]
	segWildcard                // 数组通配，如 [*]
)

type segment struct {
	kind  segKind
	key   string
	index int
}

func (s segment) String() string {
	switch s.kind {
	case segIndex:
		return "[" + strconv.Itoa(s.index) + "]"
	case segWildcard:
		return "[*]"
	default:
		return s.key
	}
}

func pathString(segs []segment) string {
	var b strings.Builder
	for i, s := range segs {
		if s.kind == segKey && i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(s.String())
	}
	return b.String()
}

// parsePath 解析点号路径，支持 a.b、a[0].b、a[*].b，以及用 \ 转义的字面点号（a\.b）。
// 空字符串返回 nil 段列表，调用方按“无路径”处理。
func parsePath(p string) ([]segment, error) {
	if strings.TrimSpace(p) == "" {
		return nil, nil
	}

	var (
		segs       []segment
		buf        strings.Builder
		lastWasDot bool
		r          = []rune(p)
	)
	flush := func() {
		if buf.Len() > 0 {
			segs = append(segs, segment{kind: segKey, key: buf.String()})
			buf.Reset()
		}
	}

	for i := 0; i < len(r); i++ {
		switch c := r[i]; c {
		case '\\':
			if i+1 >= len(r) {
				return nil, fmt.Errorf("路径 %q 以悬空的转义符结尾", p)
			}
			buf.WriteRune(r[i+1])
			i++
			lastWasDot = false

		case '.':
			if buf.Len() == 0 && (len(segs) == 0 || lastWasDot) {
				return nil, fmt.Errorf("路径 %q 含空字段名", p)
			}
			flush()
			lastWasDot = true

		case '[':
			end := -1
			for j := i + 1; j < len(r); j++ {
				if r[j] == ']' {
					end = j
					break
				}
			}
			if end < 0 {
				return nil, fmt.Errorf("路径 %q 缺少配对的 ']'", p)
			}
			flush()
			inner := string(r[i+1 : end])
			switch {
			case inner == "*":
				segs = append(segs, segment{kind: segWildcard})
			default:
				n, err := strconv.Atoi(inner)
				if err != nil || n < 0 {
					return nil, fmt.Errorf("路径 %q 含非法下标 %q", p, inner)
				}
				segs = append(segs, segment{kind: segIndex, index: n})
			}
			i = end
			lastWasDot = false

		case ']':
			return nil, fmt.Errorf("路径 %q 含多余的 ']'", p)

		default:
			buf.WriteRune(c)
			lastWasDot = false
		}
	}
	if lastWasDot {
		return nil, fmt.Errorf("路径 %q 以 '.' 结尾", p)
	}
	flush()
	return segs, nil
}

// wildcardIndex 返回第一个通配段的位置，没有则为 -1。
func wildcardIndex(segs []segment) int {
	for i, s := range segs {
		if s.kind == segWildcard {
			return i
		}
	}
	return -1
}

func wildcardCount(segs []segment) int {
	n := 0
	for _, s := range segs {
		if s.kind == segWildcard {
			n++
		}
	}
	return n
}

// withIndex 复制 segs，并把第 at 个段替换成具体下标 idx。
func withIndex(segs []segment, at, idx int) []segment {
	out := make([]segment, len(segs))
	copy(out, segs)
	out[at] = segment{kind: segIndex, index: idx}
	return out
}

// getPath 读取路径上的值。第二个返回值区分“不存在”与“存在但为 null”。
func getPath(root any, segs []segment) (any, bool) {
	cur := root
	for _, s := range segs {
		switch s.kind {
		case segKey:
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := m[s.key]
			if !ok {
				return nil, false
			}
			cur = v
		case segIndex:
			a, ok := cur.([]any)
			if !ok || s.index >= len(a) {
				return nil, false
			}
			cur = a[s.index]
		default:
			return nil, false
		}
	}
	return cur, true
}

// setPath 把 val 写入路径，按需创建中间的对象/数组，返回（可能是新建的）根。
func setPath(root any, segs []segment, val any) (any, error) {
	if len(segs) == 0 {
		return val, nil
	}
	s := segs[0]
	switch s.kind {
	case segKey:
		m, ok := root.(map[string]any)
		if !ok {
			if root != nil {
				return nil, fmt.Errorf("无法在 %T 上写字段 %q，目标路径冲突", root, s.key)
			}
			m = map[string]any{}
		}
		child, err := setPath(m[s.key], segs[1:], val)
		if err != nil {
			return nil, err
		}
		m[s.key] = child
		return m, nil

	case segIndex:
		a, ok := root.([]any)
		if !ok {
			if root != nil {
				return nil, fmt.Errorf("无法在 %T 上写下标 [%d]，目标路径冲突", root, s.index)
			}
			a = []any{}
		}
		for len(a) <= s.index {
			a = append(a, nil)
		}
		child, err := setPath(a[s.index], segs[1:], val)
		if err != nil {
			return nil, err
		}
		a[s.index] = child
		return a, nil

	default:
		return nil, fmt.Errorf("写入路径时遇到未展开的通配符 %q", pathString(segs))
	}
}
