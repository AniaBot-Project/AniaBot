package pluginexample

import (
	"github.com/jeanhua/AniaBot/common/plugin"
)

// 插件定义
type ExamplePlugin struct {
	plugin.Meta
}

// 插件插件
func NewPlugin() *ExamplePlugin {
	return &ExamplePlugin{
		Meta: plugin.Meta{
			Name:      "示例插件",     // 这是插件名字
			HelpWords: "这是一个示例插件", // 这是插件描述，发送 /help 显示的内容
			AdminOnly: false,      // 为真则只有管理员发送 /help 才显示
			Order:     0,          // 插件执行顺序，从小到大
		},
	}
}

/* 如果插件需要接受消息事件，需解开此注释
func (p *ExamplePlugin) OnGroupMsg(bot bot.Bot, cmd *command.Command, msg message.Message) bool {
	return true
}

func (p *ExamplePlugin) OnFriendMsg(bot bot.Bot, cmd *command.Command, msg message.Message) bool {
	return true
}
*/

/* 如果插件需要初始化，需解开此注释
func (p *ExamplePlugin) Start(cfg *viper.Viper) {

}
*/

/* 如果插件需要在被卸载前清理（关闭连接、落盘、按原因清理数据），需解开此注释。
reason 为 plugin.UnloadShutdown（Bot 退出/重启，全部插件都会收到）或
plugin.UnloadUninstall（插件市场卸载，仅被卸载的插件收到）
func (p *ExamplePlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
	return nil
}
*/
