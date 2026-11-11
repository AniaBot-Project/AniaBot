// Package pluginconfig 提供配置字段的注册机制。
//
// 推荐用法：插件实现 plugin.ConfigSchemaProvider 接口，返回一个带 cfg 标签的
// 配置结构体指针，框架启动时自动完成：
//  1. 反射注册字段元信息（等价于手写 []Field），将声明了默认值的键写入
//     配置中心（仅当键不存在时，升级新增的键自动补齐）；
//  2. 让 Web 控制面板根据注册的元信息动态渲染表单——新增/移除插件
//     无需改动面板代码；
//  3. 在插件 Start 之前把配置中心的值填充进结构体（见 RegisterStruct / Load），
//     插件无需再手写 cfg.Get* 逐个读取。
//
// 低层用法：插件也可实现 plugin.ConfigRegistrar 接口，直接声明
// []pluginconfig.Field（框架自身字段与需要动态生成字段的场景使用）。
// 两种方式的注册表是同一份，键相同（不区分大小写）时后注册者原位覆盖。
//
// 注册表是进程内结构，每次启动由框架与插件重新注册，不持久化。
package pluginconfig

import (
	"strings"
	"sync"
)

// Field 描述一个配置键的元信息。
type Field struct {
	Key       string   `json:"key"`                 // 点分配置键，如 plugin.xxx.enable（统一小写）
	Label     string   `json:"label"`               // 面板表单中的显示名
	Type      string   `json:"type"`                // string | password | int | float | bool | text | strings | ints | select | multiselect | slider
	Options   []string `json:"options,omitempty"`   // select / multiselect 类型：可选项列表
	Min       float64  `json:"min,omitempty"`       // slider 类型：最小值（缺省 0）
	Max       float64  `json:"max,omitempty"`       // slider 类型：最大值（缺省 100）
	Step      float64  `json:"step,omitempty"`      // slider 类型：步长（缺省 1）
	Ticks     []string `json:"ticks,omitempty"`     // slider 类型：刻度标签，从最小值到最大值均匀分布
	Group     string   `json:"group"`               // 面板中的分组名；支持「父分组.子分组」两级写法（"." 或 " · " 分隔），同一父分组在面板中合为一张卡片、子分组在卡片内分节展示
	Help      string   `json:"help,omitempty"`      // 字段说明（可选）
	Sensitive bool     `json:"sensitive,omitempty"` // 敏感字段：面板不回显，API 掩码处理
	Optional  bool     `json:"optional,omitempty"`  // 可选字段（Go 指针标量）：留空/清空表示未配置，不向下游传参
	Default   any      `json:"default,omitempty"`   // 默认值：注册时若配置中心无此键则写入；nil/空表示无默认值（面板据此决定是否允许「留空」）
}

var (
	mu     sync.RWMutex
	fields []Field
	index  = map[string]int{}
)

// Register 注册配置字段。键相同（不区分大小写）时后者原位覆盖前者，保持注册顺序。
func Register(fs ...Field) {
	mu.Lock()
	defer mu.Unlock()
	for _, f := range fs {
		f.Key = strings.ToLower(f.Key)
		if i, ok := index[f.Key]; ok {
			fields[i] = f
			continue
		}
		index[f.Key] = len(fields)
		fields = append(fields, f)
	}
}

// Fields 返回全部已注册字段（按注册顺序）。
func Fields() []Field {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Field, len(fields))
	copy(out, fields)
	return out
}

// Defaults 返回所有声明了默认值的键值对（Default 为 nil 的键不包含）。
func Defaults() map[string]any {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if f.Default != nil {
			out[f.Key] = f.Default
		}
	}
	return out
}
