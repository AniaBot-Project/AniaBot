// 结构体绑定：插件只需声明一个带 `cfg` 标签的配置结构体，框架启动时自动
// 注册字段（面板渲染 + 默认值补齐），并在插件 Start 前把配置中心的值填充进去。
//
//	type diceConfig struct {
//		MaxPoint int `cfg:"plugin.dice.max_point" label:"最大点数" group:"骰子" default:"6"`
//	}
//
//	func (p *DicePlugin) ConfigSchema() any { return &p.cfg }
//
// 支持的字段标签：
//
//	cfg       点分配置键（必填；嵌套结构体字段作为前缀段递归；`cfg:"-"` 跳过）
//	label     面板显示名（缺省用字段名）
//	type      覆盖类型推断（password / text / select / multiselect / slider 等必须显式声明）
//	options   select / multiselect 类型的可选项，逗号分隔
//	min/max/step  slider 类型的范围与步长（缺省 0 / 100 / 1）
//	ticks     slider 类型的刻度标签，逗号分隔，从 min 到 max 均匀分布
//	group     面板分组；支持「父分组.子分组」两级写法（如 "骰子.规则"），
//	          "." 或 " · " 均可作分隔符，面板按「一个父分组一张卡片、
//	          子分组在卡片内以分节框归类」展示
//	help      字段说明
//	sensitive "true" 时按敏感字段处理（面板不回显）
//	default   默认值（字符串形式，按字段类型解析；切片逗号分隔；
//	          string 类可用 \n 等转义表达多行文本；不声明则键不预填）
//	指针标量且无默认值的字段（*int、*float64）注册为可选参数（schema 中
//	optional=true），仅当配置键已设置时才下发；面板留空/清空该字段会删除对应键。
//
// 类型推断：string→string、bool→bool、int 系→int、float 系→float、
// []string→strings、[]int→ints、指针标量同底层类型；其余类型报错。
// 指针标量字段（如 *int、*float64）在 Load 时仅当配置键已设置才分配赋值，
// 保持 nil 即「未配置」语义（如可选的 LLM 采样参数）。
package pluginconfig

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

// fieldBinding 一个叶子配置字段的元信息 + 反射索引路径。
type fieldBinding struct {
	field Field
	index []int
	typ   reflect.Type // 叶子字段类型（指针字段为指针类型本身）
}

// RegisterStruct 反射读取结构体的 cfg 标签，等价地注册一组 Field
// （供面板渲染与默认值补齐）。ptr 必须是结构体指针；标签非法时返回 error。
func RegisterStruct(ptr any) error {
	bindings, err := parseSchema(ptr)
	if err != nil {
		return err
	}
	fs := make([]Field, len(bindings))
	for i, b := range bindings {
		fs[i] = b.field
	}
	Register(fs...)
	return nil
}

// Load 按 cfg 标签把 viper 中的值填充到结构体。
// 配置键未设置（IsSet=false）时保持字段零值，指针字段保持 nil。
func Load(v *viper.Viper, ptr any) error {
	bindings, err := parseSchema(ptr)
	if err != nil {
		return err
	}
	rv := reflect.ValueOf(ptr).Elem()
	for _, b := range bindings {
		if !v.IsSet(b.field.Key) {
			continue
		}
		fv := rv.FieldByIndex(b.index)
		if err := setValue(v, b.field.Key, fv, b.typ); err != nil {
			return fmt.Errorf("pluginconfig: 键 %s: %w", b.field.Key, err)
		}
	}
	return nil
}

// parseSchema 解析结构体的全部叶子字段绑定。ptr 必须是结构体指针。
func parseSchema(ptr any) ([]fieldBinding, error) {
	if ptr == nil {
		return nil, fmt.Errorf("pluginconfig: 配置结构体不能为 nil")
	}
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("pluginconfig: 需要结构体指针，得到 %T", ptr)
	}
	var out []fieldBinding
	if err := walkStruct(rv.Elem().Type(), "", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// walkStruct 递归遍历结构体字段。prefix 为键前缀（含末尾点或为空），
// indexPrefix 为嵌套字段的反射索引路径。
func walkStruct(t reflect.Type, prefix string, indexPrefix []int, out *[]fieldBinding) error {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			if sf.Tag.Get("cfg") != "" {
				return fmt.Errorf("pluginconfig: 字段 %s 带 cfg 标签但未导出", sf.Name)
			}
			continue
		}
		key := sf.Tag.Get("cfg")
		if key == "-" {
			continue
		}
		idx := append(append([]int{}, indexPrefix...), i)

		// 嵌套结构体：cfg 段作为前缀递归；无 cfg 标签则透明沿用父前缀
		if sf.Type.Kind() == reflect.Struct {
			seg := key
			if seg != "" {
				seg = strings.Trim(seg, ".") + "."
			}
			if err := walkStruct(sf.Type, prefix+seg, idx, out); err != nil {
				return err
			}
			continue
		}

		if key == "" {
			return fmt.Errorf("pluginconfig: 字段 %s 缺少 cfg 标签", sf.Name)
		}
		b, err := buildBinding(sf, prefix+key, idx)
		if err != nil {
			return err
		}
		*out = append(*out, b)
	}
	return nil
}

// buildBinding 由一个叶子字段构造绑定（类型推断 + 标签元信息 + default 解析）。
func buildBinding(sf reflect.StructField, key string, idx []int) (fieldBinding, error) {
	typ := sf.Type
	base := typ
	if base.Kind() == reflect.Pointer {
		base = base.Elem()
	}

	fieldType, err := inferFieldType(sf, base)
	if err != nil {
		return fieldBinding{}, err
	}

	f := Field{
		Key:     strings.ToLower(key),
		Label:   sf.Tag.Get("label"),
		Type:    fieldType,
		Group:   sf.Tag.Get("group"),
		Help:    sf.Tag.Get("help"),
		Options: splitList(sf.Tag.Get("options")),
		Ticks:   splitList(sf.Tag.Get("ticks")),
	}
	if f.Label == "" {
		f.Label = sf.Name
	}
	if sf.Tag.Get("sensitive") == "true" {
		f.Sensitive = true
	}
	if (fieldType == "select" || fieldType == "multiselect") && len(f.Options) == 0 {
		return fieldBinding{}, fmt.Errorf("pluginconfig: 字段 %s (%s): %s 类型必须声明 options", sf.Name, key, fieldType)
	}
	if fieldType == "slider" {
		if err := applySliderRange(sf, &f); err != nil {
			return fieldBinding{}, err
		}
	}

	if raw, ok := sf.Tag.Lookup("default"); ok && raw != "" {
		def, err := parseDefault(raw, base)
		if err != nil {
			return fieldBinding{}, fmt.Errorf("pluginconfig: 字段 %s (%s): %w", sf.Name, key, err)
		}
		f.Default = def
	}
	// 指针标量且无默认值=可选参数：未配置/清空时不向下游传（有默认值的指针字段
	// 如 max_token 仍按既有语义：缺失时补默认值，清空面板表现为置 0）
	f.Optional = typ.Kind() == reflect.Pointer && f.Default == nil

	return fieldBinding{field: f, index: idx, typ: typ}, nil
}

// applySliderRange 解析 slider 类型的 min/max/step 标签并校验取值范围。
// 三个标签都可省略：min=0、max=100、step=1。
func applySliderRange(sf reflect.StructField, f *Field) error {
	f.Min, f.Max, f.Step = 0, 100, 1
	for tag, dst := range map[string]*float64{"min": &f.Min, "max": &f.Max, "step": &f.Step} {
		raw := strings.TrimSpace(sf.Tag.Get(tag))
		if raw == "" {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("pluginconfig: 字段 %s: 解析 %s=%q 失败", sf.Name, tag, raw)
		}
		*dst = v
	}
	if f.Max <= f.Min {
		return fmt.Errorf("pluginconfig: 字段 %s: slider 的 max(%v) 必须大于 min(%v)", sf.Name, f.Max, f.Min)
	}
	if f.Step <= 0 || f.Step > f.Max-f.Min {
		return fmt.Errorf("pluginconfig: 字段 %s: slider 的 step(%v) 必须在 (0, max-min] 内", sf.Name, f.Step)
	}
	return nil
}

// inferFieldType 由 Go 类型推断 Field.Type，type 标签可覆盖。
func inferFieldType(sf reflect.StructField, base reflect.Type) (string, error) {
	var inferred string
	switch base.Kind() {
	case reflect.String:
		inferred = "string"
	case reflect.Bool:
		inferred = "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		inferred = "int"
	case reflect.Float32, reflect.Float64:
		inferred = "float"
	case reflect.Slice:
		switch base.Elem().Kind() {
		case reflect.String:
			inferred = "strings"
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			inferred = "ints"
		}
	}
	if inferred == "" {
		return "", fmt.Errorf("pluginconfig: 字段 %s 类型 %s 不支持（仅支持 string/bool/int/float/[]string/[]int 及其指针）", sf.Name, sf.Type)
	}

	override := sf.Tag.Get("type")
	if override == "" {
		return inferred, nil
	}
	switch override {
	case "string", "int", "float", "bool", "strings", "ints":
		return override, nil
	case "password", "text", "select":
		if base.Kind() != reflect.String {
			return "", fmt.Errorf("pluginconfig: 字段 %s: type=%s 要求字段为 string 类型", sf.Name, override)
		}
		return override, nil
	case "slider":
		switch base.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Float32, reflect.Float64:
			return override, nil
		}
		return "", fmt.Errorf("pluginconfig: 字段 %s: type=slider 要求字段为 int 或 float 类型", sf.Name)
	case "multiselect":
		if base.Kind() != reflect.Slice || base.Elem().Kind() != reflect.String {
			return "", fmt.Errorf("pluginconfig: 字段 %s: type=multiselect 要求字段为 []string 类型", sf.Name)
		}
		return override, nil
	default:
		return "", fmt.Errorf("pluginconfig: 字段 %s: 未知 type %q", sf.Name, override)
	}
}

// parseDefault 把 default 标签的字符串值按字段类型解析为原生值。
func parseDefault(raw string, base reflect.Type) (any, error) {
	switch base.Kind() {
	case reflect.String:
		// 支持 \n 等转义表达多行文本；无转义时原样
		if unq, err := strconv.Unquote(`"` + strings.ReplaceAll(raw, `"`, `\"`) + `"`); err == nil {
			return unq, nil
		}
		return raw, nil
	case reflect.Bool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("解析 default %q 为 bool 失败", raw)
		}
		return v, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("解析 default %q 为 int 失败", raw)
		}
		return v, nil
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("解析 default %q 为 float 失败", raw)
		}
		return v, nil
	case reflect.Slice:
		parts := splitList(raw)
		switch base.Elem().Kind() {
		case reflect.String:
			return parts, nil
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			out := make([]int, 0, len(parts))
			for _, p := range parts {
				v, err := strconv.ParseInt(p, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("解析 default 元素 %q 为 int 失败", p)
				}
				out = append(out, int(v))
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("类型 %s 不支持 default 标签", base)
}

// splitList 逗号分隔并逐元素 TrimSpace；空串返回 nil。
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// setValue 从 viper 读取一个键并写入字段。指针字段仅在到达这里时分配
// （调用方已保证 IsSet=true），nil 即「未配置」。
func setValue(v *viper.Viper, key string, fv reflect.Value, typ reflect.Type) error {
	isPtr := typ.Kind() == reflect.Pointer
	base := typ
	if isPtr {
		base = typ.Elem()
	}

	target := fv
	if isPtr {
		target = reflect.New(base).Elem()
	}

	switch base.Kind() {
	case reflect.String:
		target.SetString(v.GetString(key))
	case reflect.Bool:
		target.SetBool(v.GetBool(key))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		target.SetInt(v.GetInt64(key))
	case reflect.Float32, reflect.Float64:
		target.SetFloat(v.GetFloat64(key))
	case reflect.Slice:
		switch base.Elem().Kind() {
		case reflect.String:
			target.Set(reflect.ValueOf(v.GetStringSlice(key)))
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			ints := v.GetIntSlice(key)
			out := reflect.MakeSlice(base, len(ints), len(ints))
			for i, n := range ints {
				out.Index(i).SetInt(int64(n))
			}
			target.Set(out)
		default:
			return fmt.Errorf("不支持的切片类型 %s", base)
		}
	default:
		return fmt.Errorf("不支持的类型 %s", base)
	}

	if isPtr {
		ptr := reflect.New(base)
		ptr.Elem().Set(target)
		fv.Set(ptr)
	}
	return nil
}
