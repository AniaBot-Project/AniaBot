package pluginconfig

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/spf13/viper"
)

type nestedConfig struct {
	Enable bool   `cfg:"enable" label:"启用" group:"子组" default:"true"`
	Mode   string `cfg:"mode" label:"模式" type:"select" options:"a,b,c" group:"子组" default:"b"`
}

type sampleConfig struct {
	Name     string       `cfg:"plugin.demo.name" label:"名称" group:"演示" default:"demo"`
	Secret   string       `cfg:"plugin.demo.secret" label:"密钥" type:"password" sensitive:"true" group:"演示"`
	Multi    string       `cfg:"plugin.demo.multi" type:"text" group:"演示" default:"第一行\n第二行"`
	Count    int          `cfg:"plugin.demo.count" group:"演示" default:"42"`
	Ratio    float64      `cfg:"plugin.demo.ratio" group:"演示" default:"0.95"`
	Tags     []string     `cfg:"plugin.demo.tags" group:"演示" default:"foo, bar ,baz"`
	IDs      []int        `cfg:"plugin.demo.ids" group:"演示" default:"1,2,3"`
	MaxToken *int         `cfg:"plugin.demo.max_token" group:"演示" default:"8192"`
	TopP     *float64     `cfg:"plugin.demo.top_p" group:"演示"`
	Nested   nestedConfig `cfg:"plugin.demo.nested"`
	Plain    plainNested  // 无 cfg 标签：透明嵌套，沿用父前缀
	Skipped  string       `cfg:"-"`
	NoTagSet string       `cfg:"plugin.demo.no_tag"`
}

type plainNested struct {
	Sub string `cfg:"plugin.demo.plain.sub" group:"演示" default:"sub"`
}

func TestRegisterStruct(t *testing.T) {
	var cfg sampleConfig
	if err := RegisterStruct(&cfg); err != nil {
		t.Fatalf("RegisterStruct: %v", err)
	}

	byKey := map[string]Field{}
	for _, f := range Fields() {
		byKey[f.Key] = f
	}

	checks := []struct {
		key       string
		fieldType string
		def       any
	}{
		{"plugin.demo.name", "string", "demo"},
		{"plugin.demo.secret", "password", nil},
		{"plugin.demo.multi", "text", "第一行\n第二行"},
		{"plugin.demo.count", "int", int64(42)},
		{"plugin.demo.ratio", "float", 0.95},
		{"plugin.demo.tags", "strings", []string{"foo", "bar", "baz"}},
		{"plugin.demo.ids", "ints", []int{1, 2, 3}},
		{"plugin.demo.max_token", "int", int64(8192)},
		{"plugin.demo.nested.enable", "bool", true},
		{"plugin.demo.nested.mode", "select", "b"},
		{"plugin.demo.plain.sub", "string", "sub"},
	}
	for _, c := range checks {
		f, ok := byKey[c.key]
		if !ok {
			t.Errorf("键 %s 未注册", c.key)
			continue
		}
		if f.Type != c.fieldType {
			t.Errorf("键 %s: Type = %q, 期望 %q", c.key, f.Type, c.fieldType)
		}
		if !reflect.DeepEqual(f.Default, c.def) {
			t.Errorf("键 %s: Default = %#v, 期望 %#v", c.key, f.Default, c.def)
		}
	}

	if !byKey["plugin.demo.secret"].Sensitive {
		t.Error("secret 应为敏感字段")
	}
	if got := byKey["plugin.demo.nested.mode"].Options; !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("select Options = %v", got)
	}
	if _, ok := byKey["plugin.demo.no_tag"]; !ok {
		t.Error("无 default 的字段也应注册")
	}
	if byKey["plugin.demo.no_tag"].Default != nil {
		t.Error("无 default 标签时 Default 应为 nil")
	}
	// 可选参数标记：指针且无默认值=true；有默认值的指针与普通标量=false
	if !byKey["plugin.demo.top_p"].Optional {
		t.Error("top_p（指针、无默认值）应为可选参数")
	}
	if byKey["plugin.demo.max_token"].Optional {
		t.Error("max_token（指针但有默认值）不应视为可选参数")
	}
	if byKey["plugin.demo.count"].Optional {
		t.Error("count（普通标量）不应视为可选参数")
	}
	// Defaults() 只含声明了默认值的键
	if _, ok := Defaults()["plugin.demo.no_tag"]; ok {
		t.Error("Defaults() 不应包含无默认值的键")
	}
}

// TestFieldJSONExposesDefault 面板表单依赖 schema 中的 default 字段判断
// select 是否允许「留空」（无默认值=可选，如 subagent/备用模型的 api_format）。
func TestFieldJSONExposesDefault(t *testing.T) {
	var cfg sampleConfig
	if err := RegisterStruct(&cfg); err != nil {
		t.Fatalf("RegisterStruct: %v", err)
	}
	byKey := map[string]Field{}
	for _, f := range Fields() {
		byKey[f.Key] = f
	}

	data, err := json.Marshal(byKey["plugin.demo.name"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(data, []byte(`"default":"demo"`)) {
		t.Errorf("有默认值的字段应暴露 default, got %s", data)
	}

	data, err = json.Marshal(byKey["plugin.demo.no_tag"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(data, []byte(`"default"`)) {
		t.Errorf("无默认值的字段不应暴露 default, got %s", data)
	}
}

func TestLoad(t *testing.T) {
	var cfg sampleConfig
	if err := RegisterStruct(&cfg); err != nil {
		t.Fatalf("RegisterStruct: %v", err)
	}

	v := viper.New()
	// 模拟 EnsureDefaults 播种
	for k, val := range Defaults() {
		v.Set(k, val)
	}
	// 用户覆盖
	v.Set("plugin.demo.name", "自定义")
	v.Set("plugin.demo.count", 7)

	if err := Load(v, &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Name != "自定义" {
		t.Errorf("Name = %q", cfg.Name)
	}
	if cfg.Count != 7 {
		t.Errorf("Count = %d", cfg.Count)
	}
	if cfg.Multi != "第一行\n第二行" {
		t.Errorf("Multi = %q", cfg.Multi)
	}
	if !reflect.DeepEqual(cfg.Tags, []string{"foo", "bar", "baz"}) {
		t.Errorf("Tags = %v", cfg.Tags)
	}
	if !reflect.DeepEqual(cfg.IDs, []int{1, 2, 3}) {
		t.Errorf("IDs = %v", cfg.IDs)
	}
	if !cfg.Nested.Enable || cfg.Nested.Mode != "b" {
		t.Errorf("Nested = %+v", cfg.Nested)
	}
	if cfg.Plain.Sub != "sub" {
		t.Errorf("Plain.Sub = %q", cfg.Plain.Sub)
	}
	// 指针字段：有默认值 → 非 nil；无默认值未设置 → nil
	if cfg.MaxToken == nil || *cfg.MaxToken != 8192 {
		t.Errorf("MaxToken = %v", cfg.MaxToken)
	}
	if cfg.TopP != nil {
		t.Errorf("TopP 应保持 nil, 得到 %v", *cfg.TopP)
	}
	// 未设置的标量保持零值
	if cfg.NoTagSet != "" {
		t.Errorf("NoTagSet = %q", cfg.NoTagSet)
	}

	// 指针字段被显式设置后应被填充
	v.Set("plugin.demo.top_p", 0.5)
	if err := Load(v, &cfg); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TopP == nil || *cfg.TopP != 0.5 {
		t.Errorf("TopP = %v", cfg.TopP)
	}
}

func TestMultiselect(t *testing.T) {
	type multiConfig struct {
		Platforms []string `cfg:"plugin.demo.platforms" label:"可用平台" type:"multiselect" options:"qq,telegram,feishu" group:"演示" default:"qq,telegram,feishu"`
	}
	var cfg multiConfig
	if err := RegisterStruct(&cfg); err != nil {
		t.Fatalf("RegisterStruct: %v", err)
	}
	byKey := map[string]Field{}
	for _, f := range Fields() {
		byKey[f.Key] = f
	}
	f, ok := byKey["plugin.demo.platforms"]
	if !ok {
		t.Fatal("multiselect 字段未注册")
	}
	if f.Type != "multiselect" {
		t.Errorf("Type = %q, 期望 multiselect", f.Type)
	}
	if len(f.Options) != 3 || f.Options[0] != "qq" || f.Options[2] != "feishu" {
		t.Errorf("Options = %v", f.Options)
	}

	v := viper.New()
	for k, val := range Defaults() {
		v.Set(k, val)
	}
	var loaded multiConfig
	if err := Load(v, &loaded); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Platforms) != 3 || loaded.Platforms[0] != "qq" {
		t.Errorf("默认值未填充, Platforms = %v", loaded.Platforms)
	}
	// 用户取消勾选部分平台后应原样读回
	v.Set("plugin.demo.platforms", []string{"qq"})
	var partial multiConfig
	if err := Load(v, &partial); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(partial.Platforms) != 1 || partial.Platforms[0] != "qq" {
		t.Errorf("部分选择未读回, Platforms = %v", partial.Platforms)
	}
}

func TestSlider(t *testing.T) {
	type sliderConfig struct {
		Activity int     `cfg:"plugin.demo.activity" label:"活跃度" type:"slider" min:"0" max:"100" step:"5" ticks:"冷淡,克制,均衡,活跃,话痨" group:"演示" default:"50"`
		Plain    float64 `cfg:"plugin.demo.plain_slider" type:"slider" group:"演示"`
	}
	var cfg sliderConfig
	if err := RegisterStruct(&cfg); err != nil {
		t.Fatalf("RegisterStruct: %v", err)
	}
	byKey := map[string]Field{}
	for _, f := range Fields() {
		byKey[f.Key] = f
	}

	f, ok := byKey["plugin.demo.activity"]
	if !ok {
		t.Fatal("slider 字段未注册")
	}
	if f.Type != "slider" {
		t.Errorf("Type = %q, 期望 slider", f.Type)
	}
	if f.Min != 0 || f.Max != 100 || f.Step != 5 {
		t.Errorf("范围 = (%v,%v,%v), 期望 (0,100,5)", f.Min, f.Max, f.Step)
	}
	if want := []string{"冷淡", "克制", "均衡", "活跃", "话痨"}; !reflect.DeepEqual(f.Ticks, want) {
		t.Errorf("Ticks = %v, 期望 %v", f.Ticks, want)
	}
	if f.Default != int64(50) {
		t.Errorf("Default = %#v, 期望 50", f.Default)
	}

	// 省略 min/max/step 时应回落到 0/100/1，面板据此渲染范围控件
	plain := byKey["plugin.demo.plain_slider"]
	if plain.Min != 0 || plain.Max != 100 || plain.Step != 1 {
		t.Errorf("缺省范围 = (%v,%v,%v), 期望 (0,100,1)", plain.Min, plain.Max, plain.Step)
	}

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(data, []byte(`"max":100`)) || !bytes.Contains(data, []byte(`"step":5`)) {
		t.Errorf("slider 应暴露 max/step, got %s", data)
	}

	v := viper.New()
	for k, val := range Defaults() {
		v.Set(k, val)
	}
	var loaded sliderConfig
	if err := Load(v, &loaded); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Activity != 50 {
		t.Errorf("Activity = %d, 期望 50", loaded.Activity)
	}
	// 滑条值按普通数值读取（Load 不关心 slider 这一展示类型）
	v.Set("plugin.demo.activity", 85)
	v.Set("plugin.demo.plain_slider", 0.25)
	var updated sliderConfig
	if err := Load(v, &updated); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if updated.Activity != 85 || updated.Plain != 0.25 {
		t.Errorf("滑条值未读回: %d / %v", updated.Activity, updated.Plain)
	}
}

func TestSliderErrors(t *testing.T) {
	cases := []struct {
		name string
		ptr  any
	}{
		{"max 不大于 min", &struct {
			N int `cfg:"a.b" type:"slider" min:"10" max:"10"`
		}{}},
		{"step 非正", &struct {
			N int `cfg:"a.b" type:"slider" min:"0" max:"10" step:"0"`
		}{}},
		{"step 超过区间", &struct {
			N int `cfg:"a.b" type:"slider" min:"0" max:"10" step:"20"`
		}{}},
		{"非法 min", &struct {
			N int `cfg:"a.b" type:"slider" min:"abc"`
		}{}},
		{"slider 非数值", &struct {
			S string `cfg:"a.b" type:"slider"`
		}{}},
	}
	for _, c := range cases {
		if err := RegisterStruct(c.ptr); err == nil {
			t.Errorf("%s: 期望报错", c.name)
		}
	}
}

func TestParseSchemaErrors(t *testing.T) {
	cases := []struct {
		name string
		ptr  any
	}{
		{"nil", nil},
		{"非指针", sampleConfig{}},
		{"非结构体指针", new(int)},
		{"缺 cfg 标签", &struct {
			A string `label:"x"`
		}{}},
		{"不支持的类型", &struct {
			M map[string]string `cfg:"a.b"`
		}{}},
		{"结构体指针字段", &struct {
			N *nestedConfig `cfg:"a.b"`
		}{}},
		{"select 缺 options", &struct {
			S string `cfg:"a.b" type:"select"`
		}{}},
		{"multiselect 缺 options", &struct {
			S []string `cfg:"a.b" type:"multiselect"`
		}{}},
		{"multiselect 非 []string", &struct {
			S string `cfg:"a.b" type:"multiselect" options:"a,b"`
		}{}},
		{"password 非 string", &struct {
			P int `cfg:"a.b" type:"password"`
		}{}},
		{"非法 default int", &struct {
			N int `cfg:"a.b" default:"abc"`
		}{}},
		{"非法 default bool", &struct {
			B bool `cfg:"a.b" default:"notabool"`
		}{}},
		{"非法 default ints 元素", &struct {
			L []int `cfg:"a.b" default:"1,x"`
		}{}},
		{"未导出字段带标签", &struct {
			a string `cfg:"a.b"`
		}{}},
	}
	for _, c := range cases {
		if err := RegisterStruct(c.ptr); err == nil {
			t.Errorf("%s: 期望报错", c.name)
		}
	}
}
