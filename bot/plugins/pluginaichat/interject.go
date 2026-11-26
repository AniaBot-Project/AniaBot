package pluginaichat

import (
	"strings"

	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 运行中插话（next-step 注入）：回合正在执行时到达的新消息不再等整轮结束，
// 而是注入正在运行的 LLM 工具循环——工具循环在每次模型请求前调用
// ChatOptions.Steer 排空待处理队列，把消息作为 user 消息追加到当前对话尾部，
// 模型下一步就能看到并回应（与子代理 steering 同一机制，见 aichat/toolexecutor.go）。
//
// 队列仍是唯一来源：插话直接从 pendingQueue 取出（与运行器批次结束后的 drain
// 互斥），没来得及注入的剩余消息照旧由运行器作为下一批处理。同一消息只会被
// 其中一条路径消费（不会重复处理）；正常路径下不会丢失——消息在最后一次模型
// 请求开始后才到达时，留在队列里由运行器开下一批；/stop、取消与错误路径维持
// 旧行为：清空待处理队列。
//
// 合成消息（子代理完成通知、工作流汇总、/resume 恢复提示等，Sender.UserId=0）
// 同样走插话通道，让后台结果不必等主回合彻底结束才回到上下文。
//
// 例外：#新对话 是批次级语义（整批开始前清空历史），不能在回合中途执行——
// 在途请求仍带着旧历史，中途清空会让本批后续请求上下文不一致。插话遇到
// #新对话 时把它及其之后的消息放回队列，等本批结束后的下一批统一处理。

const (
	// interjectionUserHeader 用户插话的注入前缀：明确来源与处理要求，
	// 避免模型把插话误当系统指令或忽略不管；需要即时答复时引导用 send_message
	// 先回一条（非流式平台不会发送工具轮的中间文本，等最终回复可能太晚）。
	interjectionUserHeader = "【用户插话】你正在处理当前任务，用户此时发来了新消息，请在本轮内纳入考虑并直接回应；如果需要立刻回复，可先用 send_message 给用户发一条再继续：\n"
	// interjectionSystemHeader 合成消息（后台任务结果等）的注入前缀：
	// 不冒充用户发言，提示模型结合当前进度处理。
	interjectionSystemHeader = "【后台消息】你在处理任务期间收到一条系统消息（如子代理完成通知），请结合当前进度处理：\n"
)

// buildInterjectionSteer 构造本轮请求的 ChatOptions.Steer 回调：
//   - 每次 LLM 请求前排空当前会话的待处理消息，按发送者类型加上插话/后台消息
//     前缀后作为 user 消息注入（过滤空文本，队列为空时不注入）；
//   - 用户消息经 extraMsg 渲染（群聊带 [nickname:… id:…] 发送者前缀），其中的
//     图片登记到本轮请求的图片注册表，使 load_images 能按哈希加载插话里的图片；
//   - 队列剩余部分不消费，由运行器在本批结束后继续处理（见 drainChatBatches）；
//   - onInjected 非空时，以未加插话前缀的渲染文本回调一次（供 Query 日志记录）。
//
// reg 为本轮请求的图片注册表（可为 nil，测试/降级场景），b 为事件来源平台
// 外观（不参与发送，只用于消息渲染与图片登记）。
func (p *AIChatPlugin) buildInterjectionSteer(b bot.Bot, id message.QID, isGroup bool, reg *imageRegistry, onInjected func([]string)) func() []string {
	return func() []string {
		if b == nil {
			return nil
		}
		msgs := p.drainPending(id, isGroup)
		if len(msgs) == 0 {
			return nil
		}
		// #新对话 必须在下一批开始前处理（清空历史不能在回合中途执行），把它及
		// 其之后的消息放回队列，前者之前的消息照常插话。
		if cut := firstNewConversationIndex(msgs); cut >= 0 {
			p.prependPending(id, isGroup, msgs[cut:])
			msgs = msgs[:cut]
			if len(msgs) == 0 {
				return nil
			}
		}
		if reg != nil {
			registerMessageImages(reg, b, msgs...)
		}
		out := make([]string, 0, len(msgs))
		rendered := make([]string, 0, len(msgs))
		for i := range msgs {
			text := strings.TrimSpace(p.extraMsg(b, msgs[i]))
			if text == "" {
				continue
			}
			rendered = append(rendered, text)
			header := interjectionUserHeader
			if msgs[i].Sender.UserId == message.FromUint64(0) {
				header = interjectionSystemHeader
			}
			out = append(out, header+text)
		}
		if len(out) == 0 {
			return nil
		}
		if onInjected != nil {
			onInjected(rendered)
		}
		if p.Logger != nil {
			p.Logger.Info("回合运行中收到新消息，以插话注入当前请求", "id", id, "is_group", isGroup, "count", len(out))
		}
		return out
	}
}

// firstNewConversationIndex 返回第一条携带 #新对话 的消息下标（无则 -1）。
// 语义与 processChatBatch 的 newConversationRequested 一致：只看消息自身的文本段。
func firstNewConversationIndex(msgs []message.Message) int {
	for i := range msgs {
		if newConversationRequested(msgs[i : i+1]) {
			return i
		}
	}
	return -1
}
