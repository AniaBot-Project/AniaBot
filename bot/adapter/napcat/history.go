package napcat

import "github.com/jeanhua/AniaBot/common/model/message"

// historyParams 组装获取历史消息（get_group_msg_history / get_friend_msg_history）的
// OneBot 参数。idKey 取 "group_id" 或 "user_id"。
//
// 带 message_seq 游标时必须显式声明回溯方向：NapCat 的内核在该参数缺省时按「锚点向
// 更新方向」取窗口，传游标拿回的是锚点之后的同一段消息，导致无论如何选游标都无法看到
// 更早的消息（NapCat issue #441；#1671 维护者确认 reverse_order 仅在带 seq/id 时生效）。
// 置 reverse_order=true 后内核才向更早方向取（PR #944 起支持）。
//
// 两个键名同时发送：2025-11 之前只认 reverseOrder，之后只把 reverse_order 计入查询
// 方向（另一键被 schema 忽略，多余键无害），兼容两代协议端。不带游标（0）时 NapCat 走
// getAioFirstViewLatestMsgs 分支，方向参数不参与，无需发送。
//
// 该参数仅适用于 NapCat：Luckylilia 的 message_seq 是真实序列号、方向本就正确，传它
// 反而会把返回列表倒序，因此不得复用本助手。
func historyParams(idKey string, id message.QID, count, message_seq int) map[string]any {
	params := map[string]any{
		idKey:         rawQQ(id),
		"count":       count,
		"message_seq": message_seq,
	}
	if message_seq != 0 {
		params["reverse_order"] = true
		params["reverseOrder"] = true
	}
	return params
}
