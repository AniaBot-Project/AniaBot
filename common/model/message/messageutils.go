package message

import (
	"encoding/json"
	"fmt"
	"strings"
)

type msgHandleOpt struct {
	getMsgFunc        func(msgId QID) (*Message, bool)
	getImageOCRFunc   func(url string) string
	getForwardMsgFunc func(msgId QID) (*[]Message, bool)
	noSenderPrefix    bool
}

type MsgOptFunc func(*msgHandleOpt)

func WithGetMsgFunc(getMsgFunc func(msgId QID) (*Message, bool)) MsgOptFunc {
	return func(o *msgHandleOpt) {
		o.getMsgFunc = getMsgFunc
	}
}

func WithGetForwardMsgFunc(getForwardMsgFunc func(msgId QID) (*[]Message, bool)) MsgOptFunc {
	return func(o *msgHandleOpt) {
		o.getForwardMsgFunc = getForwardMsgFunc
	}
}

func WithGetImageOCRFunc(f func(url string) string) MsgOptFunc {
	return func(o *msgHandleOpt) {
		o.getImageOCRFunc = f
	}
}

// WithNoSenderPrefix 不输出开头的 [nickname:… id:…] 发送者前缀。
// 用于日志展示等已单独展示发送者信息的场景。
func WithNoSenderPrefix() MsgOptFunc {
	return func(o *msgHandleOpt) {
		o.noSenderPrefix = true
	}
}

func (raw Message) FriendlyText(showUrl bool, opts ...MsgOptFunc) string {
	msgFuncs := msgHandleOpt{}
	for _, f := range opts {
		f(&msgFuncs)
	}
	return raw.friendlyText(showUrl, msgFuncs)
}

// indentUnit 嵌套内容（引用内容、合并转发节点、图片识别结果）的单层缩进。
// 嵌套块按「每层各自缩进一级」的方式拼接，缩进随嵌套深度自然累加。
const indentUnit = "  "

// indentLines 给多行文本的每一行（空行除外）加上缩进前缀。
func indentLines(text, indent string) string {
	if indent == "" || text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}

// mediaMark 生成录音 / 视频的文本标记。地址是内联负载（base64:// / data:）时
// 只留类型名：这类内容体积可达 MB 级，写进消息文本会随 LLM 上下文、落盘历史与
// 消息日志一起膨胀（与图片只保留哈希的处理一致）。
func mediaMark(kind, url string) string {
	if isInlinePayload(url) {
		return "[" + kind + "]"
	}
	return "[" + kind + ":" + url + "]"
}

// publicURL 返回可写进文本标记的地址：内联负载与本地路径都退化为空，
// 避免把 MB 级 base64 或宿主机路径塞进消息文本。
func publicURL(url string) string {
	if isInlinePayload(url) {
		return ""
	}
	return url
}

// fileDisplayName 从 file 源推导展示用文件名：内联负载与空值返回空，
// 其余（URL / 本地路径）取最后一段。用于发送时未填 name 的 file 段。
func fileDisplayName(file string) string {
	if file == "" || isInlinePayload(file) {
		return ""
	}
	if i := strings.IndexAny(file, "?#"); i >= 0 {
		file = file[:i]
	}
	file = strings.TrimRight(file, "/")
	if i := strings.LastIndexAny(file, `/\`); i >= 0 {
		file = file[i+1:]
	}
	return file
}

// IndentLines 给多行文本的每一行（空行除外）加上单层缩进（indentUnit），
// 供其他模块生成与 FriendlyText 同风格的层级文本（如工具结果里的图片识别内容）。
func IndentLines(text string) string {
	return indentLines(text, indentUnit)
}

// friendlyText 生成消息的文本表示：引用内容与合并转发节点各自缩进一级嵌入，
// 便于阅读并区分层级。
func (raw Message) friendlyText(showUrl bool, msgFuncs msgHandleOpt) string {
	var result strings.Builder
	if !msgFuncs.noSenderPrefix {
		nickname := raw.Sender.Card
		if nickname == "" {
			nickname = raw.Sender.Nickname
		}
		if nickname == "" {
			nickname = "用户" // 昵称不可得（如通讯录查询失败/权限缺失）时的兜底
		}
		result.WriteString(fmt.Sprintf("[nickname:%s id:%s]: ", nickname, raw.Sender.UserId.String()))
	}
	for _, s := range raw.Message {
		switch s.Type {
		case SegmentText:
			var msg TextMessage
			if ok := ParseText(s, &msg); ok {
				result.WriteString(msg.Text)
			}
		case SegmentFace:
			var msg FaceMessage
			if ok := ParseFace(s, &msg); ok {
				if dsc, ok2 := emojiMap[msg.Id]; ok2 {
					result.WriteString(fmt.Sprintf("[QQ表情:%s]", dsc))
				} else {
					result.WriteString(fmt.Sprintf("[QQ表情: id %d]", msg.Id))
				}
			}
		case SegmentImage:
			var msg ImageMessage
			if ok := ParseImage(s, &msg); ok {
				if msgFuncs.getImageOCRFunc != nil {
					result.WriteString(fmt.Sprintf("\n<图片消息 %s>\n", msg.Hash()))
					result.WriteString(indentLines(strings.TrimRight(msgFuncs.getImageOCRFunc(msg.Url), "\n"), indentUnit))
					result.WriteString(fmt.Sprintf("\n</图片消息 %s>\n", msg.Hash()))
				} else {
					if showUrl {
						// 同时输出短哈希与 URL：哈希用于 load_images 按需加载，
						// URL 供 AI 下载图片到本地（如 bash/file 工具）。
						// 内联负载（data: / base64://，微信/飞书/Telegram/Discord 的
						// 内联图片与 Bot 自己发出的图片）是 MB 级 base64，写进标记会随
						// 消息文本进入 LLM 上下文、落盘历史与消息日志，只保留哈希
						//（load_images 注册表仍持有完整 URI，按哈希可加载）
						if msg.Url != "" && !isInlinePayload(msg.Url) {
							result.WriteString(fmt.Sprintf("[图片 %s url:%s]", msg.Hash(), msg.Url))
						} else {
							result.WriteString(fmt.Sprintf("[图片 %s]", msg.Hash()))
						}
					} else {
						result.WriteString("[图片]")
					}
				}
			} else {
				// 无 url/file 键的图片段（如缓存剔除内联负载后）保留占位，
				// 使历史文本仍能体现「此处有图片」
				result.WriteString("[图片]")
			}
		case SegmentRecord:
			var msg RecordMessage
			if ok := ParseRecord(s, &msg); ok {
				if showUrl {
					result.WriteString(mediaMark("录音", msg.URL))
				} else {
					result.WriteString("[录音]")
				}
			} else {
				result.WriteString("[录音]")
			}
		case SegmentVideo:
			var msg VideoMessage
			if ok := ParseVideo(s, &msg); ok {
				if showUrl {
					result.WriteString(mediaMark("视频", msg.URL))
				} else {
					result.WriteString("[视频]")
				}
			} else {
				result.WriteString("[视频]")
			}
		case SegmentMention:
			var msg MentionMessage
			if ok := ParseMention(s, &msg); ok {
				if msg.QQ == raw.Sender.UserId {
					continue
				}
				if msg.IsAll {
					result.WriteString("[at:全体成员]")
				} else if msg.QQ == raw.SelfId {
					result.WriteString("[at我]")
				} else {
					// 无法解析被@用户在本群的真实昵称，仅输出其 id，避免误用发送者昵称造成张冠李戴
					result.WriteString(fmt.Sprintf("[at:id:%s]", msg.QQ))
				}
			}
		case SegmentMusic:
			var msg MusicMessage
			if ok := ParseMusic(s, &msg); ok {
				result.WriteString(fmt.Sprintf("[音乐:%s]", msg.Title))
			}
		case SegmentReply:
			var msg ReplyMessage
			if ok := ParseReply(s, &msg); ok {
				if msgFuncs.getMsgFunc != nil {
					if dtMsg, ok2 := msgFuncs.getMsgFunc(msg.Id); ok2 {
						// 引用内容缩进一级展示；被引用消息内的引用不再递归展开
						// （不传 getMsgFunc），图片识别与合并转发能力透传
						quoted := msgHandleOpt{
							getImageOCRFunc:   msgFuncs.getImageOCRFunc,
							getForwardMsgFunc: msgFuncs.getForwardMsgFunc,
						}
						result.WriteString("<reply>\n")
						result.WriteString(indentLines(strings.TrimRight(dtMsg.friendlyText(showUrl, quoted), "\n"), indentUnit))
						result.WriteString("\n</reply>\n")
					}
				}
			}
		case SegmentForward:
			// NapCat 解析转发内容时会把（含嵌套的）内容内联在 content 字段里，
			// 内层转发 id 仅供查看、无法再通过 get_forward_msg 拉取，因此优先展开内联内容；
			// 无内联内容时再回退为按 id 拉取详情
			if inline, ok := ParseForwardContent(s); ok {
				writeForwardMessages(&result, inline, showUrl, msgFuncs)
			} else if msgFuncs.getForwardMsgFunc != nil {
				var msg ForwardMessage
				if ok := ParseForward(s, &msg); ok {
					if detail, ok := msgFuncs.getForwardMsgFunc(msg.Id); ok {
						writeForwardMessages(&result, *detail, showUrl, msgFuncs)
					} else {
						result.WriteString("[转发消息, 无法获取详情]")
					}
				}
			} else {
				result.WriteString("[转发消息]")
			}
		case SegmentFile:
			var msg FileMessage
			if ok := ParseFile(s, &msg); ok {
				// 文本里只放文件名与可访问地址：file 字段是上传源（Bot 用 file 工具
				// 发送文件时是 base64:// 内联负载），原样写进标记会让整段文件内容
				// 进入消息文本、LLM 上下文与消息日志，一条日志可达数 MB
				name := msg.Name
				if name == "" {
					name = fileDisplayName(msg.File)
				}
				if name == "" {
					name = "文件"
				}
				result.WriteString(fmt.Sprintf("[文件:%s fileId:%s url:%s]",
					name, msg.FileId, publicURL(msg.URL)))
			} else {
				result.WriteString("[文件消息]")
			}
		case SegmentJson:
			var jsonMap JsonMessage
			if ok := ParseJson(s, &jsonMap); ok {
				switch jsonMap.View {
				case "news":
					news := JsonNews{}
					if err := json.Unmarshal(jsonMap.Meta, &news); err != nil {
						result.WriteString("[分享卡片: 无法获取内容]")
					} else {
						result.WriteString(fmt.Sprintf("[分享卡片,标题: %s,描述: %s,链接: (%s)]", news.News.Title, news.News.Desc, news.News.JumpUrl))
					}
				default:
					detail := JsonDetailMeta{}
					if err := json.Unmarshal(jsonMap.Meta, &detail); err != nil {
						result.WriteString("[分享卡片: 无法获取内容]")
					} else {
						result.WriteString(fmt.Sprintf("[分享卡片,标题: %s,描述: %s]", detail.Detail.Title, detail.Detail.Desc))
					}
				}
			} else {
				result.WriteString("[分享卡片: 无法获取内容]")
			}
		default:
			result.WriteString(fmt.Sprintf("[%s]", s.Type))
		}
	}
	return result.String()
}

// writeForwardMessages 输出合并转发消息内容：每个节点独占一行并缩进一级，
// 逐条渲染并透传 OCR/转发拉取回调，使内层嵌套合并转发也能继续递归展开。
func writeForwardMessages(w *strings.Builder, msgs []Message, showUrl bool, msgFuncs msgHandleOpt) {
	// 转发记录内的 reply 不再展开（内层消息 id 拉不到，只产生失败日志），
	// 与 registerMessageImages 的处理保持一致
	inner := msgHandleOpt{
		getImageOCRFunc:   msgFuncs.getImageOCRFunc,
		getForwardMsgFunc: msgFuncs.getForwardMsgFunc,
	}
	w.WriteString("\n<合并转发消息>")
	for _, msg := range msgs {
		w.WriteString("\n")
		w.WriteString(indentLines(strings.TrimRight(msg.friendlyText(showUrl, inner), "\n"), indentUnit))
	}
	w.WriteString("\n</合并转发消息>\n")
}
