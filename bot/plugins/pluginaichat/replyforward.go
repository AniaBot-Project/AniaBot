package pluginaichat

import (
	"log/slog"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/msgchain"
)

// 「按空行拆分回复」的群聊合并转发：拆分片段过多时逐条发送会在群里刷屏，
// 改为整段作为一条合并转发消息发出（每段一个节点，首节点携带 @ 提及）。
// 仅群聊生效——私聊逐条发送不会打扰他人，合并转发反而多一步展开；
// 平台不支持合并转发（仅 QQ 的 NapCat/Luckylilia，见 bot.GroupForwardSender）
// 或发送失败时保持逐条发送，功能不因平台差异而丢失。

const (
	// forwardSegThreshold 拆分片段超过该条数时改用合并转发（不超过则仍逐条发送）
	forwardSegThreshold = 3
	// forwardNodeNickname 合并转发节点的发送者昵称。节点发送者是机器人自身：
	// 平台能识别传入的机器人 ID 时显示该昵称，识别不了则用机器人真实昵称兜底
	forwardNodeNickname = "AniaBot"
)

// shouldMergeGroupReply 群聊回复是否改用合并转发：拆分出的片段数超过阈值。
// 开关关闭或没有空行时只有一条片段，逐条发送即可。
func shouldMergeGroupReply(segments []string) bool {
	return len(segments) > forwardSegThreshold
}

// sendGroupForwardReply 把群聊回复片段作为一条合并转发消息发送：每段一个节点，
// 首节点携带 @ 提及（与逐条发送时「仅首条 @」的语义一致）。平台/适配器不支持
// 合并转发或发送失败时返回 false，由调用方退化为逐条发送。
//
// sender 为机器人自身 ID（事件里的 self_id）；为空时交给平台按自身身份兜底。
func sendGroupForwardReply(b bot.Bot, groupId message.QID, segments []string, mentions []message.QID, sender message.QID, logger *slog.Logger) bool {
	fs, ok := b.(bot.GroupForwardSender)
	if !ok {
		return false
	}
	forward := msgchain.Builder().GroupForward()
	for i, seg := range segments {
		node := msgchain.Builder().Group()
		if i == 0 {
			for _, uid := range mentions {
				node.Mention(uid)
			}
			if len(mentions) > 0 {
				node.Text(" " + seg)
			} else {
				node.Text(seg)
			}
		} else {
			node.Text(seg)
		}
		forward.Message(sender, forwardNodeNickname, node.Build())
	}
	if _, success := fs.SendGroupForwardMsg(groupId, forward.Build()); !success {
		// 非 QQ 平台（不支持合并转发）与 QQ 发送失败都走这里，回退逐条发送
		logger.Info("当前平台不支持合并转发或发送失败，改为逐条发送", "group", groupId, "segments", len(segments))
		return false
	}
	logger.Info("发送合并转发回复", "group", groupId, "segments", len(segments), "mentions", len(mentions))
	return true
}

// batchSelfID 本批消息里机器人自身的 ID（QQ 为 self_id，即机器人 QQ 号）；
// 平台未下发时为空，合并转发按平台自身身份兜底。
func batchSelfID(batch []message.Message) message.QID {
	for i := range batch {
		if batch[i].SelfId != "" {
			return batch[i].SelfId
		}
	}
	return ""
}
