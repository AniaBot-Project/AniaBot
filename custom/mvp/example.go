// custom/mvp 最小可运行示例。
//
// 平台适配器走注册表：空白导入适配器包触发 init() 注册，与 cmd/main.go 的
// 接入方式一致。启用的平台由 Web 面板配置键 bot.platform.<name>.enable 决定
// （默认仅 QQ/NapCat），NapCat 的 ws/http 子模式仍由 bot.adapter.mode 配置。
package main

import (
	"context"

	_ "github.com/jeanhua/AniaBot/bot/adapter/napcat" // 空白导入触发 NapCat 适配器注册
	"github.com/jeanhua/AniaBot/bot/core"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/command"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

func main() {
	bot := core.NewAniaBot()

	// 自定义插件
	bot.AddPlugin(NewCustomPlugin())

	bot.Run()
}

type CustomPlugin struct {
	plugin.Meta
}

func NewCustomPlugin() *CustomPlugin {
	return &CustomPlugin{
		Meta: plugin.Meta{
			Name:      "自定义插件",
			HelpWords: "这是一个自定义插件",
			AdminOnly: false,
			ShowFor:   plugininfo.ShowForGroup | plugininfo.ShowForFriend, // 显示范围，不设置则 /help 不显示
			Order:     0,
		},
	}
}

// 接收群聊消息事件
func (p *CustomPlugin) OnGroupMsg(ctx context.Context, bot bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
	if cmd.Mention && cmd.Name == "test" { // 判断条件：@[bot] /test 有效
		builder := msgchain.Builder().Group()          // 群消息构造器
		builder.Text("测试成功")                           // 构造消息
		bot.SendGroupMsg(msg.GroupId, builder.Build()) // 发送消息
		return false, nil                              // 已处理，不再传给后续插件
	}
	return true, nil // 表示继续执行下一个插件
}
