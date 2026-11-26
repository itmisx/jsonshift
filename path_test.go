package jsonshift

import "testing"

func TestParsePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"a", "a", true},
		{"a.b.c", "a.b.c", true},
		{"a[0].b", "a[0].b", true},
		{"a[*].b[*]", "a[*].b[*]", true},
		{"[0].a", "[0].a", true},
		{`a\.b.c`, "a.b.c", true}, // 转义点号后 String() 不再区分，仅验证可解析
		{"a..b", "", false},
		{".a", "", false},
		{"a.", "", false},
		{"a[", "", false},
		{"a]", "", false},
		{"a[x]", "", false},
		{"a[-1]", "", false},
		{`a\`, "", false},
	}
	for _, c := range cases {
		segs, err := parsePath(c.in)
		if c.ok != (err == nil) {
			t.Fatalf("parsePath(%q): err=%v, 期望 ok=%v", c.in, err, c.ok)
		}
		if c.ok && pathString(segs) != c.want {
			t.Errorf("parsePath(%q) = %q, 期望 %q", c.in, pathString(segs), c.want)
		}
	}
}

func TestParsePathEscape(t *testing.T) {
	segs, err := parsePath(`a\.b.c`)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 || segs[0].key != "a.b" || segs[1].key != "c" {
		t.Fatalf("转义解析错误: %+v", segs)
	}
}

func TestGetSetPath(t *testing.T) {
	segs, _ := parsePath("a.b[1].c")
	root, err := setPath(nil, segs, 42)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := getPath(root, segs)
	if !ok || got != 42 {
		t.Fatalf("取回 %v (ok=%v)，期望 42", got, ok)
	}
	// [1] 之前的洞应为 null
	hole, _ := parsePath("a.b[0]")
	if v, ok := getPath(root, hole); !ok || v != nil {
		t.Fatalf("空洞 = %v (ok=%v)，期望 null", v, ok)
	}
}

func TestSetPathConflict(t *testing.T) {
	segs, _ := parsePath("a")
	root, _ := setPath(nil, segs, "标量")
	deeper, _ := parsePath("a.b")
	if _, err := setPath(root, deeper, 1); err == nil {
		t.Fatal("在标量上写子字段应报错")
	}
}

func TestGetPathMissing(t *testing.T) {
	root := map[string]any{"a": []any{1, 2}}
	for _, p := range []string{"b", "a[5]", "a.b", "a[0].x"} {
		segs, _ := parsePath(p)
		if _, ok := getPath(root, segs); ok {
			t.Errorf("%q 应判定为不存在", p)
		}
	}
}

func TestGetPathNullIsPresent(t *testing.T) {
	root := map[string]any{"a": nil}
	segs, _ := parsePath("a")
	v, ok := getPath(root, segs)
	if !ok || v != nil {
		t.Fatalf("显式 null 应视为存在: v=%v ok=%v", v, ok)
	}
}
