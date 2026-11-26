package jsonshift_test

import (
	"fmt"

	"github.com/itmisx/jsonshift"
)

// 只改路径时，配置就是一张平铺表：键是设备侧路径，值是平台侧路径，
// 从左到右读正好是上行的流向。同一份配置双向可用。
func Example_flatForm() {
	m, err := jsonshift.ParseMapping([]byte(`{
      "sn":      "deviceId",
      "ch[*].t": "properties.channels[*].temperature",
      "errs":    "properties.alarmCodes[*].code"
    }`))
	if err != nil {
		panic(err)
	}
	s, _ := jsonshift.New(m)

	// 上行：设备上报 -> 平台标准格式
	std, _ := s.Uplink([]byte(`{
      "sn": "SN-001",
      "ch": [{"t": 23.5}, {"t": 61.2}],
      "errs": ["E01", "E07"]
    }`))
	fmt.Println(string(std))

	// 下行：同一份配置反向跑，把平台格式转回设备要的形状
	dev, _ := s.Downlink(std)
	fmt.Println(string(dev))

	// Output:
	// {"deviceId":"SN-001","properties":{"alarmCodes":[{"code":"E01"},{"code":"E07"}],"channels":[{"temperature":23.5},{"temperature":61.2}]}}
	// {"ch":[{"t":23.5},{"t":61.2}],"errs":["E01","E07"],"sn":"SN-001"}
}

// 需要缩放、枚举一类的值变换时，把这些属性挪到 fields；两种写法可以混用。
func Example_iotProperty() {
	cfg := []byte(`{
      "schema": "github.com/itmisx/jsonshift",
      "name":   "vendor-x-thermostat",
      "map": {
        "dev_sn": "deviceId",
        "sw":     "properties.firmwareVersion",

        "temp":  {"key": "properties.temperature", "value": "{temp}/10",
                  "comment": "设备以 0.1℃ 为单位传整数；下行自动求逆成 {.}*10"},
        "degf":  {"key": "properties.ambientC",    "value": "({degf}-32)*5/9",
                  "comment": "设备报华氏度，平台存摄氏度"},
        "sw_st": {"key": "properties.powerSwitch", "enum": [["ON", 1], ["OFF", 0]]},
        "rssi":  {"key": "properties.rssi",        "convert": "~string2int",
                  "comment": "设备把信号强度塞在字符串里上报"},
        "md":    {"key": "properties.mode",
                  "enum": [["cool", 1], ["heat", 2], ["auto", 3]], "default": "auto"}
      }
    }`)

	m, err := jsonshift.ParseMapping(cfg)
	if err != nil {
		panic(err) // 后台保存配置时就该拦住
	}
	s, err := jsonshift.New(m) // 预编译一次，按产品型号缓存复用
	if err != nil {
		panic(err)
	}

	// 上行：设备只报了部分属性，没报的自动跳过，不会凭空出现在平台数据里
	up, _ := s.Uplink([]byte(`{"dev_sn":"SN-001","temp":218,"degf":77,"sw_st":0,"rssi":"-67"}`))
	fmt.Println(string(up))

	// 下行用的是自动求出的逆表达式，可以显示给配置者确认
	fmt.Println("temp 下行表达式:", s.DerivedReverse("temp"))
	fmt.Println("degf 下行表达式:", s.DerivedReverse("degf"))

	// 下行：平台标准格式 -> 设备格式
	down, _ := s.Downlink([]byte(`{
      "deviceId": "SN-001",
      "properties": {"temperature": 23.5, "powerSwitch": "ON", "mode": "cool"}
    }`))
	fmt.Println(string(down))

	// Output:
	// {"deviceId":"SN-001","properties":{"ambientC":25,"mode":"auto","powerSwitch":"OFF","rssi":-67,"temperature":21.8}}
	// temp 下行表达式: {.}*10
	// degf 下行表达式: (({.}*9)/5)+32
	// {"dev_sn":"SN-001","md":1,"sw_st":1,"temp":235}
}

// 厂商的量纲换算五花八门，内置转换器不够时自己挂一对可逆函数。
func Example_customConverter() {
	m := &jsonshift.Mapping{
		Fields: []jsonshift.Field{
			// 设备用 0.5kWh 为单位的累计值
			{A: "properties.energy", B: "kwh", Convert: "halfKWh"},
			{A: "properties.name", B: "nm", Default: "未命名"},
		},
		Converters: map[string]jsonshift.Converter{
			"halfKWh": {
				Forward: func(v any) (any, error) { return int64(v.(float64) * 2), nil },
				Reverse: func(v any) (any, error) { return v.(float64) / 2, nil },
			},
		},
	}

	s, err := jsonshift.New(m)
	if err != nil {
		panic(err)
	}

	std, _ := s.Uplink([]byte(`{"kwh":25}`))
	fmt.Println(string(std))

	dev, _ := s.Downlink(std)
	fmt.Println(string(dev))

	// Output:
	// {"properties":{"energy":12.5,"name":"未命名"}}
	// {"kwh":25,"nm":"未命名"}
}
