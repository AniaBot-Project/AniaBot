package pluginaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/textproto"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/aichat"
	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/aitool"
	"github.com/jeanhua/AniaBot/common/bot"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/spf13/viper"
	"golang.org/x/net/http/httpguts"
)

// botQQ 返回 bot 的 QQ 平台专属能力（事件来源为 QQ 适配器时可用），否则返回 nil。
func botQQ(b bot.Bot) bot.QQ {
	if qb, ok := b.(bot.QQ); ok {
		return qb
	}
	return nil
}

// parseQID 解析目标 ID：纯数字（QQ 群号/QQ号）规范化为 qq: 前缀 QID，
// 其他（多平台带前缀，如飞书 fs:oc_xxx）原样保留。
func parseQID(s string) message.QID {
	if n, err := strconv.ParseUint(s, 10, 64); err == nil {
		return message.FromUint64(n)
	}
	return message.FromString(strings.TrimSpace(s))
}

// parseHeaderLines 解析自定义请求头配置（plugin.ai_chat_bot.headers），
// 支持 "Name: Value" 与 "Name=Value" 两种写法：按行内最先出现的分隔符切分，
// 值中可自由包含另一种字符（如 "Authorization: Bearer a=b"）。空行忽略；
// 缺分隔符、名字为空或名字/值含非法字符（net/http 会在发请求时拒绝，坏配置
// 会拖垮全部 LLM 请求）的行跳过，并以 1 基行号返回给调用方记录告警（只记
// 行号不记内容，避免把误填的凭据写进日志）。名字统一规范化
// （X-Custom-Token），大小写不同视为同一条，重复以最后一行为准。
func parseHeaderLines(lines []string) (headers map[string]string, invalidLines []int) {
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		idx := strings.IndexAny(line, ":=")
		if idx <= 0 {
			invalidLines = append(invalidLines, i+1)
			continue
		}
		name := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
			invalidLines = append(invalidLines, i+1)
			continue
		}
		if headers == nil {
			headers = make(map[string]string, len(lines))
		}
		// 规范化名字：与 net/http 的 Set 行为一致，也保证大小写不同的重复
		// 名字合并为一条（map 遍历顺序随机，不合并会让最终生效值不确定）
		headers[textproto.CanonicalMIMEHeaderKey(name)] = value
	}
	return headers, invalidLines
}

// headerNames 返回请求头名字的升序列表（日志用，避免把凭据值写进日志）。
func headerNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (p *AIChatPlugin) extraMsg(b bot.Bot, msg message.Message) string {
	opts := []message.MsgOptFunc{
		message.WithGetMsgFunc(b.GetMsgDetail),
	}
	// 合并转发展开属 QQ 平台能力：仅事件来源为 QQ 时挂载，其余平台回退占位符
	if qb := botQQ(b); qb != nil {
		opts = append(opts, message.WithGetForwardMsgFunc(qb.GetForwardMsg))
	}
	// 合成消息（如子代理结果，UserId 为 0）不附加 [nickname:… id:…] 发送者前缀，
	// 其正文已自带身份标识
	if msg.Sender.UserId == message.FromUint64(0) {
		opts = append(opts, message.WithNoSenderPrefix())
	}
	// 文本内嵌附件描述（如 QQ 官方聊天记录）兜底补充 [图片 <hash> url:<url>] 标记
	return aitool.AnnotateEmbeddedImages(msg.FriendlyText(true, opts...))
}

// imageRef 已解析到的图片引用：哈希用于与消息文本中的 [图片 <hash>] 标记对应，
// URL 用于真正加载图片进上下文（多模态）或交给备用识别模型（OCR）。
type imageRef struct {
	Hash string
	URL  string
}

// imageRegistryKey 请求上下文中图片注册表的键：平台历史消息工具
// （aitool.NewMsgHistoryTool）执行时经它取当前请求的注册表登记图片，
// 注册表在 processChatBatch 创建后随请求上下文传递。
type imageRegistryKey struct{}

// imageRegistry 请求级图片哈希→URL 注册表。消息展示给 AI 的每张图片
// （当前消息、历史消息记录、合并转发内容）都会登记，
// load_images 按哈希查找并只加载指定的图片，避免一次全部塞进上下文。
type imageRegistry struct {
	mu      sync.Mutex
	byHash  map[string]string
	byAlias map[string]string // 非哈希别名（如聊天记录文本里的文件名）→ URL
}

func newImageRegistry() *imageRegistry {
	return &imageRegistry{
		byHash:  make(map[string]string),
		byAlias: make(map[string]string),
	}
}

// register 登记图片哈希→URL 映射；同一哈希只保留首次登记（先到先得）。
func (r *imageRegistry) register(hash, url string) {
	if hash == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byHash[hash]; !ok {
		r.byHash[hash] = url
	}
}

// registerAlias 登记图片的非哈希别名（如聊天记录文本中的文件名）→ URL，
// 兼容 AI 把文件名当哈希传入 load_images 的情况；同一别名只保留首次登记。
func (r *imageRegistry) registerAlias(alias, url string) {
	if alias == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byAlias[alias]; !ok {
		r.byAlias[alias] = url
	}
}

// resolve 按哈希解析图片引用，返回找到的引用与未登记的哈希。
// 未命中哈希时回退查文件名别名，命中后返回规范哈希（与消息文本 [图片 <hash>] 一致）。
func (r *imageRegistry) resolve(hashes []string) (found []imageRef, missing []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, hash := range hashes {
		url, ok := r.byHash[hash]
		if !ok {
			if aliasURL, ok2 := r.byAlias[hash]; ok2 {
				found = append(found, imageRef{Hash: message.ImageHash(aliasURL), URL: aliasURL})
				continue
			}
			missing = append(missing, hash)
			continue
		}
		found = append(found, imageRef{Hash: hash, URL: url})
	}
	return found, missing
}

// imageMessageSource 消息图片登记所需的最小消息查询能力（bot.Bot 天然满足）。
type imageMessageSource interface {
	GetMsgDetail(msgId message.QID) (*message.Message, bool)
}

// imageForwardSource 合并转发展开能力（QQ 平台独有，bot.Bot 未必实现）。
type imageForwardSource interface {
	GetForwardMsg(msgId message.QID) (*[]message.Message, bool)
}

// registerMessageImages 递归登记消息及其引用/合并转发中的图片（与 FriendlyText
// 的展开范围一致），使 load_images 能按哈希加载历史记录与转发里的图片。
func registerMessageImages(reg *imageRegistry, src imageMessageSource, msgs ...message.Message) {
	if reg == nil {
		return
	}
	seen := make(map[message.QID]struct{})
	for _, msg := range msgs {
		reg.registerMessage(src, msg, seen, true)
	}
}

// registerMessage 递归登记单条消息中的图片。
// resolveReplies=false 用于合并转发记录里的消息：其中的 reply 目标多为聊天记录
// 内部的历史消息，无法再通过 get_msg 拉取，跳过可避免大量「消息不存在或已被
// 撤回」的请求失败日志。
func (r *imageRegistry) registerMessage(src imageMessageSource, current message.Message, seen map[message.QID]struct{}, resolveReplies bool) {
	if current.MessageId != "" {
		if _, ok := seen[current.MessageId]; ok {
			return
		}
		seen[current.MessageId] = struct{}{}
	}
	for _, segment := range current.Message {
		switch segment.Type {
		case message.SegmentImage:
			var image message.ImageMessage
			if message.ParseImage(segment, &image) {
				r.register(image.Hash(), image.Url)
			}
		case message.SegmentText:
			// 平台文本内嵌的图片附件描述（如 QQ 官方聊天记录）兜底登记，
			// 使 load_images 在适配器未拆段时也能按哈希/文件名加载
			var txt message.TextMessage
			if message.ParseText(segment, &txt) {
				registerEmbeddedImages(r, txt.Text)
			}
		case message.SegmentReply:
			if resolveReplies {
				var reply message.ReplyMessage
				if message.ParseReply(segment, &reply) {
					if detail, ok := src.GetMsgDetail(reply.Id); ok && detail != nil {
						r.registerMessage(src, *detail, seen, true)
					}
				}
			}
		case message.SegmentForward:
			// NapCat 会把（含嵌套的）转发内容内联在 forward 段 content 里且内层 id
			// 无法再拉取，优先登记内联内容中的图片；无内联内容时再回退按 id 拉取
			if inline, ok := message.ParseForwardContent(segment); ok {
				for i := range inline {
					// 转发记录内的消息不解析 reply（历史消息 ID 拉不到，只产生失败日志）
					r.registerMessage(src, inline[i], seen, false)
				}
			} else {
				var fwd message.ForwardMessage
				if message.ParseForward(segment, &fwd) {
					if fwdSrc, ok := src.(imageForwardSource); ok {
						if detail, ok := fwdSrc.GetForwardMsg(fwd.Id); ok && detail != nil {
							for i := range *detail {
								r.registerMessage(src, (*detail)[i], seen, false)
							}
						}
					}
				}
			}
		}
	}
}

// registerEmbeddedImages 登记文本内嵌图片附件的哈希→URL，并登记文件名别名，
// 使 load_images 能加载聊天记录等以文本描述携带的图片。
func registerEmbeddedImages(r *imageRegistry, text string) {
	for _, img := range aitool.ScanEmbeddedImages(text) {
		r.register(message.ImageHash(img.URL), img.URL)
		if img.Filename != "" {
			r.registerAlias(img.Filename, img.URL)
		}
	}
}

// configureImageCallbacks 挂载消息图片的加载回调。registry 为本次请求的
// 图片哈希→URL 注册表（当前消息、历史记录、合并转发中的图片都会登记），
// load_images 按哈希解析并只加载指定的图片。usageSink 接收备用图片识别
// （OCR）产生的 LLM 用量，由调用方并入所属请求/会话的统计与配额。
func (p *AIChatPlugin) configureImageCallbacks(ctx context.Context, bot bot.Bot, callbacks *llmtool.CallBackFuncs, registry *imageRegistry, usageSink func(aichat.TokenUsage), msgs ...message.Message) {
	registerMessageImages(registry, bot, msgs...)
	var loadedImages []string
	// loadedHashes 记录已加载过的图片哈希，避免同一张图片重复进入上下文/重复 OCR；
	// 仅在图片成功取回后登记，加载失败的哈希下次可重试
	loadedHashes := make(map[string]struct{})

	callbacks.LoadImages = func(hashes []string) (string, error) {
		if len(hashes) == 0 {
			return "请通过 hashes 参数传入要查看的图片哈希。图片在消息中以 [图片 <hash> url:<url>] 标识，可从当前消息、历史消息记录或合并转发内容中获取", nil
		}

		found, missing := registry.resolve(hashes)
		if len(found) == 0 {
			msg := "没有找到可加载的图片："
			if len(missing) > 0 {
				msg += "未登记的哈希 " + strings.Join(missing, "、") + "（请确认哈希来自消息中的 [图片 <hash> url:<url>] 标记）"
			}
			return msg, nil
		}

		// 只加载本次尚未加载过的图片；已加载的哈希直接跳过
		var toLoad []imageRef
		already := make([]string, 0)
		for _, ref := range found {
			if _, ok := loadedHashes[ref.Hash]; ok {
				already = append(already, "[图片 "+ref.Hash+"]")
				continue
			}
			if ref.URL == "" {
				missing = append(missing, ref.Hash+"（无可用链接）")
				continue
			}
			toLoad = append(toLoad, ref)
		}

		if len(toLoad) == 0 {
			msg := "这些图片已经加载过，无需重复调用（" + strings.Join(already, "、") + "）"
			if len(missing) > 0 {
				msg += "；未找到：" + strings.Join(missing, "、")
			}
			return msg, nil
		}

		if p.cfg.Multimodal {
			// 在本机下载并统一为 data URI：QQ 临时链接带 rkey、上游机房未必能访问，
			// 直接透传 URL 会因上游拉取失败或格式不支持（如 QQ 截图常见的 BMP）导致整轮请求 400
			labels := make([]string, 0, len(toLoad))
			failed := make([]string, 0)
			for _, ref := range toLoad {
				dataURI, err := fetchImageDataURI(ctx, ref.URL)
				if err != nil {
					p.Logger.Warn("图片加载失败", "hash", ref.Hash, "error", err.Error())
					failed = append(failed, "[图片 "+ref.Hash+"]："+err.Error())
					continue
				}
				loadedHashes[ref.Hash] = struct{}{}
				loadedImages = append(loadedImages, dataURI)
				labels = append(labels, "[图片 "+ref.Hash+"]")
			}
			var extra string
			if len(missing) > 0 {
				extra += "；未找到：" + strings.Join(missing, "、")
			}
			if len(failed) > 0 {
				extra += "；加载失败：" + strings.Join(failed, "、")
			}
			if len(loadedImages) == 0 {
				return "图片加载失败（" + strings.Join(failed, "、") + "）" + extra, nil
			}
			return fmt.Sprintf("已加载 %d 张图片（%s），图片将在下一轮上下文中提供，请直接查看图片后回答%s", len(labels), strings.Join(labels, "、"), extra), nil
		}

		if p.ocrModel == nil {
			return "当前主模型不支持加载图片，且未配置备用图片识别模型，无法查看图片内容", nil
		}

		var result strings.Builder
		result.WriteString("主模型不支持多模态，以下是备用图片识别模型返回的图片描述：")
		for _, ref := range toLoad {
			// 与多模态路径一致：先在本机下载并转码为 data URI，避免备用模型拉不到 QQ 链接
			dataURI, err := fetchImageDataURI(ctx, ref.URL)
			if err != nil {
				p.Logger.Warn("备用图片识别下载失败", "hash", ref.Hash, "error", err.Error())
				result.WriteString(fmt.Sprintf("\n<图片 %s>识别失败：%s</图片 %s>", ref.Hash, err.Error(), ref.Hash))
				continue
			}
			loadedHashes[ref.Hash] = struct{}{}
			description, usage, err := p.ocrModel.GetSingleImageDesc(ctx, "描述图片内容", dataURI, p.buildOCRChatOptions())
			if err != nil {
				p.Logger.Error("备用图片识别请求失败", "hash", ref.Hash, "error", err.Error())
				result.WriteString(fmt.Sprintf("\n<图片 %s>识别失败：%s</图片 %s>", ref.Hash, err.Error(), ref.Hash))
				continue
			}
			if usageSink != nil {
				usageSink(usage)
			}
			result.WriteString(fmt.Sprintf("\n<图片 %s>\n%s\n</图片 %s>", ref.Hash, description, ref.Hash))
		}
		if len(missing) > 0 {
			result.WriteString("\n未找到：" + strings.Join(missing, "、"))
		}
		return result.String(), nil
	}
	callbacks.TakeLoadedImages = func() []string {
		images := loadedImages
		loadedImages = nil
		return images
	}
	callbacks.LoadLocalImage = func(path string) (string, error) {
		return p.loadLocalImageInto(ctx, path, &loadedImages, usageSink), nil
	}
}

// loadLocalImageInto 读取本地图片供 LLM 查看：主模型支持多模态时把 data URI 推入
// 待加载队列（loadedImages 由调用方持有，下一轮上下文提供），否则交由备用识别模型描述。
// usageSink 接收备用识别产生的 LLM 用量（多模态路径不产生产量，不会调用）。
// 与 file 工具一致，禁止读取配置文件以避免凭据等敏感信息经图片通道泄露。
func (p *AIChatPlugin) loadLocalImageInto(ctx context.Context, path string, loadedImages *[]string, usageSink func(aichat.TokenUsage)) string {
	if strings.Contains(path, "aniabot.db") {
		return "禁止读取aniabot数据库文件"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("读取本地图片失败: %v", err)
	}
	dataURI, err := imageDataURI(data)
	if err != nil {
		return fmt.Sprintf("本地图片处理失败: %v", err)
	}
	hash := message.ImageHash(dataURI)

	if p.cfg.Multimodal {
		// data URI 推入待加载队列，下一轮由 TakeLoadedImages 取出并入上下文；
		// data URI 不依赖外部链接，历史持久化后重启也不会失效
		*loadedImages = append(*loadedImages, dataURI)
		return fmt.Sprintf("已加载本地图片 %s（[图片 %s]），将在下一轮上下文中提供，请直接查看图片后回答", path, hash)
	}

	if p.ocrModel == nil {
		return "当前主模型不支持加载图片，且未配置备用图片识别模型，无法查看图片内容"
	}
	description, usage, err := p.ocrModel.GetSingleImageDesc(ctx, "描述图片内容", dataURI, p.buildOCRChatOptions())
	if err != nil {
		p.Logger.Error("备用图片识别请求失败", "path", path, "error", err.Error())
		return fmt.Sprintf("本地图片识别失败: %v", err)
	}
	if usageSink != nil {
		usageSink(usage)
	}
	return fmt.Sprintf("主模型不支持多模态，以下是备用图片识别模型返回的图片描述：\n<图片 %s>\n%s\n</图片 %s>", hash, description, hash)
}

type mcpFileConfig struct {
	Servers []*mcpServerEntry `json:"servers"`
}

type mcpServerEntry struct {
	Name        string            `json:"name"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Endpoint    string            `json:"endpoint"`
	Headers     map[string]string `json:"headers"`
	TimeoutSecs int               `json:"timeout"`
	Description string            `json:"description"`
}

const mcpConfigKey = "files.mcp_json"

// loadMCPConfigs 从配置中心读取 MCP 服务器配置（原 aniabot.mcp.json 的原始 JSON 文本）。
func (p *AIChatPlugin) loadMCPConfigs(cfg *viper.Viper) error {
	raw := cfg.GetString(mcpConfigKey)
	if strings.TrimSpace(raw) == "" {
		p.Logger.Info("未配置 MCP 服务器，跳过 MCP 加载", "key", mcpConfigKey)
		return nil
	}

	var fileCfg mcpFileConfig
	if err := json.Unmarshal([]byte(raw), &fileCfg); err != nil {
		return fmt.Errorf("解析 MCP 配置失败: %w", err)
	}

	if len(fileCfg.Servers) == 0 {
		p.Logger.Info("MCP 配置中未配置任何服务器")
		return nil
	}

	for i, entry := range fileCfg.Servers {
		if entry.Name == "" {
			p.Logger.Warn("MCP 服务器配置缺少名称", "index", i)
			continue
		}

		mcpConfig, err := mcpEntryToConfig(entry)
		if err != nil {
			p.Logger.Warn("MCP 服务器配置无效", "name", entry.Name, "error", err.Error())
			continue
		}

		p.mcpConfigs = append(p.mcpConfigs, mcpConfig)
		if mcpConfig.Endpoint != "" {
			p.Logger.Info("已加载 MCP 服务器配置", "name", mcpConfig.Name, "transport", mcpConfig.Transport, "endpoint", mcpConfig.Endpoint)
		} else {
			p.Logger.Info("已加载 MCP 服务器配置", "name", mcpConfig.Name, "command", mcpConfig.Command)
		}
	}

	p.Logger.Info("MCP 服务器配置加载完成", "count", len(p.mcpConfigs))
	return nil
}

// mcpEntryToConfig 将持久化条目转换为运行时 MCP 配置并做基本校验
// （HTTP 模式缺 endpoint / stdio 模式缺 command 均视为无效）。
func mcpEntryToConfig(entry *mcpServerEntry) (*llmtool.MCPConfig, error) {
	mcpConfig := &llmtool.MCPConfig{
		Name:        entry.Name,
		Transport:   entry.Transport,
		Command:     entry.Command,
		Args:        entry.Args,
		Env:         entry.Env,
		Endpoint:    entry.Endpoint,
		Headers:     entry.Headers,
		Description: entry.Description,
	}
	if entry.TimeoutSecs > 0 {
		mcpConfig.Timeout = time.Duration(entry.TimeoutSecs) * time.Second
	}

	transport := strings.ToLower(mcpConfig.Transport)
	isHTTP := transport == "streamable" || transport == "streamable-http" || transport == "sse"
	if isHTTP {
		if mcpConfig.Endpoint == "" {
			return nil, fmt.Errorf("缺少 endpoint")
		}
	} else if mcpConfig.Command == "" {
		return nil, fmt.Errorf("缺少 command")
	}
	return mcpConfig, nil
}

func (p *AIChatPlugin) thinkingOpts() aichat.ChatOptions {
	if !p.cfg.Thinking.Enable {
		return aichat.ChatOptions{}
	}

	effort := p.cfg.Thinking.Mode
	if effort == "" || effort == "auto" {
		return aichat.ChatOptions{}
	}

	validEfforts := map[string]bool{"low": true, "medium": true, "high": true}
	if !validEfforts[effort] {
		effort = "low"
	}

	return aichat.ChatOptions{
		ReasoningEffort: &effort,
	}
}
