package pluginaichat

import (
	"fmt"
	"log/slog"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// 消息类工具回调（发送文本/图片/文件、私聊文件链接等）。
// 历史消息查询不再是回调：原 get_msg_history 已删除，由平台注入的历史消息工具
//（aitool.NewMsgHistoryTool，经 platformtools.go 注入）替代，图片登记经
// Context.OnMessages + 请求上下文中的 imageRegistryKey 完成。
//
// splitBlankLine 为「按空行拆分回复」开关：SendText 同样遵循，开关开启时一段
// 文本按空行拆成多条消息（仅首条 @ 用户）。框架不再自动调用 SendText 发送工具轮
// 中间文本（非流式平台只发最终回复，流式平台由 streamReply 覆盖式展示），
// 该回调保留给工具/宿主显式发送消息。

func MakeGroupCallback(bot bot.Bot, groupId, userId message.QID, logger *slog.Logger, splitBlankLine bool) llmtool.CallBackFuncs {
	msgFuncs := llmtool.CallBackFuncs{
		SendText: func(s string) (string, error) {
			for i, seg := range replySegments(s, splitBlankLine) {
				builder := msgchain.Builder().Group()
				if i == 0 {
					builder.Mention(userId)
					builder.Text(" " + seg)
				} else {
					builder.Text(seg)
				}
				if _, success := bot.SendGroupMsg(groupId, builder.Build()); !success {
					return "", fmt.Errorf("发送失败")
				}
				logger.Info("发送文本", "group", groupId, "user", userId, "segment", i+1, "text", seg)
			}
			return "发送成功", nil
		},
		SendImage: func(bs64content string) (string, error) {
			builder := msgchain.Builder().Group()
			builder.ImageBase64(bs64content)
			_, success := bot.SendGroupMsg(groupId, builder.Build())
			if success {
				logger.Info("发送图片", "group", groupId, "user", userId)
				return "发送成功", nil
			}
			return "", fmt.Errorf("发送失败")
		},
		SendFile: func(name, bs64content string) (string, error) {
			builder := msgchain.Builder().Group()
			builder.FileBase64(name, bs64content)
			_, success := bot.SendGroupMsg(groupId, builder.Build())
			if success {
				logger.Info("发送文件", "group", groupId, "user", userId, "file", name)
				return "发送成功", nil
			}
			// 失败原因可能是上传超时（见适配器上传超时配置）：内容可能稍后仍会送达，
			// 提示模型勿自动重发，避免用户收到重复文件
			return "", fmt.Errorf("发送失败（也可能是上传超时、内容稍后仍会送达，请勿重复发送）")
		},
		GetPrivateFileURL: func(fileId string) (string, error) {
			qb := botQQ(bot)
			if qb == nil {
				return "", fmt.Errorf("当前平台不支持获取私聊文件URL")
			}
			url, ok := qb.GetPrivateFileURL(userId, fileId)
			if !ok {
				return "", fmt.Errorf("获取私聊文件URL失败")
			}
			return url, nil
		},
	}

	return msgFuncs
}

func MakeFriendCallback(bot bot.Bot, userId message.QID, logger *slog.Logger, splitBlankLine bool) llmtool.CallBackFuncs {
	msgFuncs := llmtool.CallBackFuncs{
		SendText: func(s string) (string, error) {
			for i, seg := range replySegments(s, splitBlankLine) {
				builder := msgchain.Builder().Friend()
				builder.Text(seg)
				if _, success := bot.SendFriendMsg(userId, builder.Build()); !success {
					return "", fmt.Errorf("发送失败")
				}
				logger.Info("发送文本", "user", userId, "segment", i+1, "text", seg)
			}
			return "发送成功", nil
		},
		SendImage: func(bs64content string) (string, error) {
			builder := msgchain.Builder().Friend()
			builder.ImageBase64(bs64content)
			_, success := bot.SendFriendMsg(userId, builder.Build())
			if success {
				logger.Info("发送图片", "user", userId)
				return "发送成功", nil
			}
			return "", fmt.Errorf("发送失败")
		},
		SendFile: func(name, bs64content string) (string, error) {
			builder := msgchain.Builder().Friend()
			builder.FileBase64(name, bs64content)
			_, success := bot.SendFriendMsg(userId, builder.Build())
			if success {
				logger.Info("发送文件", "user", userId, "file", name)
				return "发送成功", nil
			}
			// 失败原因可能是上传超时（见适配器上传超时配置）：内容可能稍后仍会送达，
			// 提示模型勿自动重发，避免用户收到重复文件
			return "", fmt.Errorf("发送失败（也可能是上传超时、内容稍后仍会送达，请勿重复发送）")
		},
		GetPrivateFileURL: func(fileId string) (string, error) {
			qb := botQQ(bot)
			if qb == nil {
				return "", fmt.Errorf("当前平台不支持获取私聊文件URL")
			}
			url, ok := qb.GetPrivateFileURL(userId, fileId)
			if !ok {
				return "", fmt.Errorf("获取私聊文件URL失败")
			}
			return url, nil
		},
	}
	return msgFuncs
}
