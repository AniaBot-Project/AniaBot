package pluginrepeat

// repeatConfig 复读机插件的配置结构体。实现 plugin.ConfigSchemaProvider 后，
// 框架启动时自动注册字段（面板渲染 + 默认值补齐），并在 Start 前填充完成。
type repeatConfig struct {
	Enable bool `cfg:"plugin.repeat.enable" label:"启用复读机" group:"复读机插件" help:"关闭后不再自动复读；也可用 /close repeat、/enable repeat 命令开关（会同步写回此配置）" default:"true"`
	// TriggerCount 连续多少条相同消息触发一次跟读。小于 2 的配置无意义
	// （只收到一条消息时无法判定重复），运行时按 2 处理。
	TriggerCount int `cfg:"plugin.repeat.trigger_count" label:"触发条数" group:"复读机插件" help:"同一条消息连续出现多少条时自动跟读一次（至少 2，默认 3）" default:"3"`
}

// ConfigSchema 实现 plugin.ConfigSchemaProvider：返回配置结构体指针，
// 框架在 Start 前自动完成注册与填充，Start 里直接读 p.cfg。
func (p *RepeatPlugin) ConfigSchema() any {
	return &p.cfg
}
