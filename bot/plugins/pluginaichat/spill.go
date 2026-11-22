package pluginaichat

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// attachToolResultLimit 为一次请求的工具回调挂载结果治理闭包：
// 超长成功结果落盘并回填预览 + handle；read_tool_output 经同一闭包读回。
// sessionKey 决定落盘目录（按会话隔离，读回无法跨会话）。
func (p *AIChatPlugin) attachToolResultLimit(cbs *llmtool.CallBackFuncs, sessionKey string) {
	if p == nil || cbs == nil || p.toolResultLimiter == nil {
		return
	}
	limiter := p.toolResultLimiter
	cbs.LimitToolResult = func(toolName, result string, isError bool) string {
		return limiter.Limit(sessionKey, toolName, result, isError)
	}
	cbs.ReadToolOutput = func(handle string, offset, limit int, fromEnd bool, pattern string) (string, error) {
		return limiter.Read(sessionKey, handle, offset, limit, fromEnd, pattern)
	}
}

// 工具结果长度治理：所有工具回填给 AI 的结果都经过同一道出口
//（aichat.ToolOrchestrator）。超过内联上限的成功结果会写入会话临时文件，
// 回填"预览 + handle + 读取指引"，由 AI 调用 read_tool_output 按需读取；
// 落盘失败、错误文本或功能关闭时退化为头尾截断。
//
// 设计参考主流 coding agent 的 "spill to file" 模式：直接截断会丢掉日志/测试
// 结果的中段（往往正是答案所在），且工具结果会随窗口历史每轮重发。落盘后
// 上下文只保留固定大小的预览，历史体积、Query 日志体积与后续每轮 prompt 都随之变小。

const (
	// spillJanitorInterval 落盘文件清理的轮询周期。
	spillJanitorInterval = 10 * time.Minute
	// spillReadMaxLines read_tool_output 单次最多返回的行数。
	spillReadMaxLines = 2000
	// spillReadDefaultLines read_tool_output 默认返回行数。
	spillReadDefaultLines = 200
	// spillReadLineMaxRunes read_tool_output 单行展示上限（与 read_file 同口径）。
	spillReadLineMaxRunes = 2000
	// spillReadMaxRunes read_tool_output 单次输出总上限：读回工具自身也要有界，
	// 否则刚省下来的上下文会被一次"读全部"重新灌满。
	spillReadMaxRunes = 8000
	// spillReadExemptMaxRunes read_tool_output 结果免于落盘的 rune 上限（硬上限
	// 加读取提示余量）。按上限判断而不是只看工具名：若某个 MCP 服务器恰好也叫
	// read_tool_output，它返回超大结果时仍会被统一治理，不能借名字绕过。
	spillReadExemptMaxRunes = spillReadMaxRunes + 512
	// spillPatternMaxLines pattern 过滤最多返回的匹配行数。
	spillPatternMaxLines = 200
	// spillPatternMaxRunes pattern 子串长度上限（字面量，非正则，避免病态回溯）。
	spillPatternMaxRunes = 64

	spillDefaultInlineRunes    = 16000
	spillDefaultPreviewRunes   = 4000
	spillDefaultMaxFileMB      = 16
	spillDefaultRetentionHours = 24
	spillDefaultMaxTotalMB     = 512
	spillMinInlineRunes        = 100
)

// spillHandleRe handle 允许的字符集：会话目录内的文件名即 handle+".txt"，
// 只接受安全字符可从根上杜绝路径穿越。
var spillHandleRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// toolResultLimiter 工具结果长度治理器。插件级单例，回调按会话闭包调用。
type toolResultLimiter struct {
	cfg    toolResultConfig
	store  *spillStore // nil：不落盘（功能关闭或落盘目录初始化失败）
	logger *slog.Logger
}

// newToolResultLimiter 构造治理器：规范化阈值；启用落盘时初始化存储目录
// （目录不可用时回退系统临时目录，仍失败则退化为纯截断）。
func newToolResultLimiter(cfg toolResultConfig, logger *slog.Logger) *toolResultLimiter {
	if logger == nil {
		logger = slog.Default()
	}
	l := &toolResultLimiter{cfg: cfg, logger: logger}
	if l.cfg.MaxInlineRunes <= 0 {
		l.cfg.MaxInlineRunes = spillDefaultInlineRunes
	}
	if l.cfg.MaxInlineRunes < spillMinInlineRunes {
		l.cfg.MaxInlineRunes = spillMinInlineRunes
	}
	if l.cfg.PreviewRunes <= 0 {
		l.cfg.PreviewRunes = spillDefaultPreviewRunes
	}
	// 预览不能达到/超过内联上限，否则"落盘"没有节省上下文的意义
	if l.cfg.PreviewRunes >= l.cfg.MaxInlineRunes {
		l.cfg.PreviewRunes = l.cfg.MaxInlineRunes / 4
		if l.cfg.PreviewRunes < spillMinInlineRunes {
			l.cfg.PreviewRunes = spillMinInlineRunes
		}
	}
	if l.cfg.Enable {
		l.store = newSpillStore(cfg, logger)
	}
	return l
}

// SpillEnabled 是否具备落盘能力（决定是否注册 read_tool_output 与启动清理器）。
func (l *toolResultLimiter) SpillEnabled() bool {
	return l != nil && l.store != nil
}

// InlineRunes 返回生效的内联上限（日志/面板展示用）。
func (l *toolResultLimiter) InlineRunes() int {
	if l == nil {
		return 0
	}
	return l.cfg.MaxInlineRunes
}

// PreviewRunes 返回生效的预览长度（日志/面板展示用）。
func (l *toolResultLimiter) PreviewRunes() int {
	if l == nil {
		return 0
	}
	return l.cfg.PreviewRunes
}

// StoreDir 返回落盘根目录（未启用落盘时为空）。
func (l *toolResultLimiter) StoreDir() string {
	if l == nil || l.store == nil {
		return ""
	}
	return l.store.baseDir
}

// Limit 对一次工具结果做统一长度治理。
//
//	sessionKey: 会话键（g:/f:/panelchat:...），决定落盘目录
//	toolName:   工具名（写入 handle 前缀，便于 AI 判断结果来源）
//	content:    工具返回文本
//	isError:    是否错误结果（错误只截断、不落盘：模型必须当轮看到失败原因）
func (l *toolResultLimiter) Limit(sessionKey, toolName, content string, isError bool) string {
	if l == nil || content == "" {
		return content
	}
	maxInline := l.cfg.MaxInlineRunes
	// 字节数必然 ≥ rune 数：先按字节快速放行，避免对每个正常结果都全量数 rune
	if len(content) <= maxInline {
		return content
	}
	// read_tool_output 自身结果已有硬上限（spillReadMaxRunes），禁止再落盘：
	// 否则读回结果超限时会生成新 handle，形成"越读越多"的递归。按"名字 +
	// 不超过硬上限"判定：真实读回结果必然有界，同名 MCP 工具的超大结果仍会被治理。
	if toolName == "read_tool_output" && utf8.RuneCountInString(content) <= spillReadExemptMaxRunes {
		return content
	}
	totalRunes := utf8.RuneCountInString(content)
	if totalRunes <= maxInline {
		return content
	}
	if isError || l.store == nil {
		return truncateMiddleRunes(content, maxInline, totalRunes)
	}
	handle, capped, err := l.store.write(sessionKey, toolName, content)
	if err != nil {
		l.logger.Warn("工具结果落盘失败，降级为头尾截断",
			"tool", toolName, "session", sessionKey, "error", err.Error())
		return truncateMiddleRunes(content, maxInline, totalRunes)
	}
	return l.buildSpillResult(handle, toolName, content, totalRunes, capped)
}

// Read 读取落盘的工具结果。参数由 read_tool_output 工具传入，这里统一钳制。
func (l *toolResultLimiter) Read(sessionKey, handle string, offset, limit int, fromEnd bool, pattern string) (string, error) {
	if l == nil || l.store == nil {
		return "", errors.New("当前未启用工具结果落盘（plugin.ai_chat_bot.tool_result.enable=false）")
	}
	return l.store.read(sessionKey, handle, offset, limit, fromEnd, pattern)
}

// Purge 删除某会话的落盘目录（#新对话 / 自动清空历史时调用，历史已无引用）。
func (l *toolResultLimiter) Purge(sessionKey string) {
	if l == nil || l.store == nil {
		return
	}
	l.store.purge(sessionKey)
}

// startJanitor 启动后台清理 goroutine。插件无 Stop 钩子，goroutine 与进程
// 同生命周期（与会话回收 janitor 的惯例一致；见 chatcache.go）。
func (l *toolResultLimiter) startJanitor() {
	if l == nil || l.store == nil {
		return
	}
	l.store.startJanitor()
}

// buildSpillResult 拼装"预览 + handle + 读取指引"回填文本。总长度约为
// preview_runes + 固定提示（~250 rune），与原始结果大小无关。capped 表示
// 落盘文件本身超过单文件上限（已保留头尾），需在提示里明确告知。
func (l *toolResultLimiter) buildSpillResult(handle, toolName, content string, totalRunes int, capped bool) string {
	preview := l.cfg.PreviewRunes
	headN := preview * 3 / 4
	tailN := preview - headN
	head := headRunes(content, headN)
	tail := tailRunes(content, tailN)
	totalLines := strings.Count(content, "\n") + 1
	omitted := totalRunes - headN - tailN
	cappedNote := ""
	if capped {
		cappedNote = fmt.Sprintf("（结果超过单文件上限 %d MB，文件内已保留头尾并标注省略）\n", l.store.maxFileBytes>>20)
	}
	return fmt.Sprintf(
		"【工具结果已落盘】handle=%s（工具 %s，共 %d 字符 / %d 行，超过内联上限 %d）\n"+
			"完整内容已保存，用 read_tool_output 读取，不要重复执行原工具：\n"+
			"%s"+
			"- 读开头：read_tool_output(handle=%q, offset=1, limit=200)\n"+
			"- 搜关键行：read_tool_output(handle=%q, pattern=\"关键词\")（字面量子串，非正则）\n"+
			"- 看结尾：read_tool_output(handle=%q, from_end=true, limit=200)\n"+
			"--- 以下为开头 %d 字符 ---\n%s\n…（中间 %d 字符已省略）…\n--- 以下为结尾 %d 字符 ---\n%s",
		handle, toolName, totalRunes, totalLines, l.cfg.MaxInlineRunes,
		cappedNote, handle, handle, handle,
		headN, head, omitted, tailN, tail)
}

// ─────────────────────────────────────────────
// 落盘存储
// ─────────────────────────────────────────────

// spillStore 会话隔离的落盘存储。
//
// 目录：<base>/<sha256(sessionKey)[:8]>/<handle>.txt。目录名用会话键哈希，
// 不在磁盘上暴露群号/QQ；handle 只含安全字符，读回时再校验一次路径归属，
// 因此 AI 无法借 handle 读取其他会话或目录外的文件。
type spillStore struct {
	baseDir       string
	maxFileBytes  int64
	maxTotalBytes int64
	retention     time.Duration
	logger        *slog.Logger
	cleanupMu     sync.Mutex
}

// newSpillStore 初始化落盘目录。默认 ./data/tool-outputs（Docker 内位于
// ./data 挂载卷），创建失败时回退系统临时目录；仍失败返回 nil（退化为截断）。
func newSpillStore(cfg toolResultConfig, logger *slog.Logger) *spillStore {
	base := strings.TrimSpace(cfg.Dir)
	if base == "" {
		base = filepath.Join("data", "tool-outputs")
	}
	if abs, err := filepath.Abs(base); err == nil {
		base = abs
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		fallback := filepath.Join(os.TempDir(), "aniabot-tool-outputs")
		if err2 := os.MkdirAll(fallback, 0o700); err2 != nil {
			logger.Error("工具结果落盘目录不可用，超长结果将直接截断",
				"dir", base, "fallback", fallback, "error", err2.Error())
			return nil
		}
		logger.Warn("工具结果落盘目录创建失败，改用系统临时目录",
			"dir", base, "fallback", fallback, "error", err.Error())
		base = fallback
	}

	maxFileMB := cfg.MaxFileMB
	if maxFileMB <= 0 {
		maxFileMB = spillDefaultMaxFileMB
	}
	hours := cfg.RetentionHours
	if hours <= 0 {
		hours = spillDefaultRetentionHours
	}
	maxTotalMB := cfg.MaxTotalMB
	if maxTotalMB <= 0 {
		maxTotalMB = spillDefaultMaxTotalMB
	}
	return &spillStore{
		baseDir:       base,
		maxFileBytes:  int64(maxFileMB) << 20,
		maxTotalBytes: int64(maxTotalMB) << 20,
		retention:     time.Duration(hours) * time.Hour,
		logger:        logger,
	}
}

// sessionDir 会话落盘目录（不存在时由 write 创建）。
func (s *spillStore) sessionDir(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return filepath.Join(s.baseDir, hex.EncodeToString(sum[:4]))
}

// write 把完整结果写入会话目录，返回 handle 与是否因单文件上限而裁剪。
// 单文件超过上限时保留头尾并写入省略标记（落盘文件是最后一份副本，尽量
// 不整段丢弃；回填提示会明确告知文件已被裁剪）。
func (s *spillStore) write(sessionKey, toolName, content string) (string, bool, error) {
	dir := s.sessionDir(sessionKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("创建落盘目录失败: %w", err)
	}
	data := []byte(content)
	capped := false
	if int64(len(data)) > s.maxFileBytes {
		data = capFileBytes(data, s.maxFileBytes)
		capped = true
	}
	for attempt := 0; attempt < 4; attempt++ {
		handle := newSpillHandle(toolName)
		path := filepath.Join(dir, handle+".txt")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue // handle 碰撞（概率极低），换一个
		}
		if err != nil {
			return "", false, fmt.Errorf("创建落盘文件失败: %w", err)
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			_ = os.Remove(path)
			return "", false, fmt.Errorf("写入落盘文件失败: %w", writeErr)
		}
		if closeErr != nil {
			_ = os.Remove(path)
			return "", false, fmt.Errorf("关闭落盘文件失败: %w", closeErr)
		}
		return handle, capped, nil
	}
	return "", false, errors.New("生成唯一 handle 失败")
}

// read 读回落盘内容并按参数分页/过滤。所有参数在此钳制，保证读回结果有界。
func (s *spillStore) read(sessionKey, handle string, offset, limit int, fromEnd bool, pattern string) (string, error) {
	handle = strings.TrimSpace(handle)
	if !spillHandleRe.MatchString(handle) {
		return "", fmt.Errorf("handle 格式非法（应形如 bash_a1b2c3d4）")
	}
	dir := filepath.Clean(s.sessionDir(sessionKey))
	path := filepath.Clean(filepath.Join(dir, handle+".txt"))
	if !strings.HasPrefix(path, dir+string(filepath.Separator)) {
		return "", errors.New("handle 非法")
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("handle %q 不存在或已过期（保留 %d 小时）；如需完整结果请重新执行原工具",
				handle, int(s.retention.Hours()))
		}
		return "", fmt.Errorf("读取落盘结果失败: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("handle 非法")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取落盘结果失败: %w", err)
	}
	if len(data) == 0 {
		return fmt.Sprintf("[工具结果 handle=%s | 文件为空]", handle), nil
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	total := len(lines)

	if p := strings.TrimSpace(pattern); p != "" {
		return s.readPattern(handle, lines, p), nil
	}

	if limit <= 0 {
		limit = spillReadDefaultLines
	}
	if limit > spillReadMaxLines {
		limit = spillReadMaxLines
	}
	if fromEnd {
		start := total - limit
		if start < 0 {
			start = 0
		}
		header := fmt.Sprintf("[工具结果 handle=%s | 共 %d 行 | 显示末尾第 %d-%d 行（from_end=true）]", handle, total, start+1, total)
		return s.formatLines(header, lines, indexRange(start, total)), nil
	}
	if offset < 1 {
		offset = 1
	}
	if offset > total {
		return fmt.Sprintf("[工具结果 handle=%s | 共 %d 行 | offset=%d 超出文件范围]", handle, total, offset), nil
	}
	end := offset - 1 + limit
	if end > total {
		end = total
	}
	header := fmt.Sprintf("[工具结果 handle=%s | 共 %d 行 | 显示第 %d-%d 行]", handle, total, offset, end)
	return s.formatLines(header, lines, indexRange(offset-1, end)), nil
}

// readPattern 按字面子串过滤行（不区分大小写，非正则）并带行号返回。
func (s *spillStore) readPattern(handle string, lines []string, pattern string) string {
	if r := []rune(pattern); len(r) > spillPatternMaxRunes {
		pattern = string(r[:spillPatternMaxRunes])
	}
	needle := strings.ToLower(pattern)
	var idx []int
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), needle) {
			idx = append(idx, i)
			if len(idx) >= spillPatternMaxLines {
				break
			}
		}
	}
	if len(idx) == 0 {
		return fmt.Sprintf("[工具结果 handle=%s | 共 %d 行 | pattern=%q 无匹配行]", handle, len(lines), pattern)
	}
	header := fmt.Sprintf("[工具结果 handle=%s | 共 %d 行 | pattern=%q 命中 %d 行（最多显示 %d 行）]",
		handle, len(lines), pattern, len(idx), spillPatternMaxLines)
	return s.formatLines(header, lines, idx)
}

// formatLines 格式化若干行：行号 + 内容，单行截断，总输出受 spillReadMaxRunes 约束。
func (s *spillStore) formatLines(header string, lines []string, idx []int) string {
	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("\n")
	budget := spillReadMaxRunes - utf8.RuneCountInString(header) - 1
	for _, i := range idx {
		if i < 0 || i >= len(lines) {
			continue
		}
		line := lines[i]
		if r := []rune(line); len(r) > spillReadLineMaxRunes {
			line = string(r[:spillReadLineMaxRunes]) + "…(行截断)"
		}
		row := fmt.Sprintf("%6d\t%s\n", i+1, line)
		rowRunes := utf8.RuneCountInString(row)
		if rowRunes > budget {
			sb.WriteString("…（本次读取达到输出上限，请增大 offset 或用 pattern 缩小范围）\n")
			break
		}
		budget -= rowRunes
		sb.WriteString(row)
	}
	return sb.String()
}

// purge 删除某会话的落盘目录（历史已清空后调用）。
func (s *spillStore) purge(sessionKey string) {
	if err := os.RemoveAll(s.sessionDir(sessionKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("清理工具结果落盘目录失败", "session", sessionKey, "error", err.Error())
	}
}

// startJanitor 启动后台清理：删除过期文件，并在总量超限时淘汰最旧文件。
func (s *spillStore) startJanitor() {
	go func() {
		ticker := time.NewTicker(spillJanitorInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.cleanup()
		}
	}()
}

// cleanup 执行一轮清理（导出给测试直接调用）。
func (s *spillStore) cleanup() {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()

	type fileEntry struct {
		path string
		size int64
		mod  time.Time
	}
	var files []fileEntry
	var total int64
	walkErr := filepath.WalkDir(s.baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil // 忽略不可读项，清理尽力而为
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		files = append(files, fileEntry{path: path, size: info.Size(), mod: info.ModTime()})
		total += info.Size()
		return nil
	})
	if walkErr != nil {
		s.logger.Warn("遍历工具结果落盘目录失败", "dir", s.baseDir, "error", walkErr.Error())
		return
	}

	now := time.Now()
	removed := 0
	for _, f := range files {
		if now.Sub(f.mod) > s.retention {
			if err := os.Remove(f.path); err == nil {
				total -= f.size
				removed++
			}
		}
	}
	if s.maxTotalBytes > 0 && total > s.maxTotalBytes {
		sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
		for _, f := range files {
			if total <= s.maxTotalBytes {
				break
			}
			if _, err := os.Stat(f.path); err != nil {
				continue // 已在上一步删除
			}
			if err := os.Remove(f.path); err == nil {
				total -= f.size
				removed++
			}
		}
	}

	// 顺手清理空会话目录，避免目录条目长期堆积
	if entries, err := os.ReadDir(s.baseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(s.baseDir, e.Name())
			if sub, err := os.ReadDir(dir); err == nil && len(sub) == 0 {
				_ = os.Remove(dir)
			}
		}
	}
	if removed > 0 {
		s.logger.Info("清理工具结果落盘文件", "removed", removed, "dir", s.baseDir)
	}
}

// ─────────────────────────────────────────────
// 工具函数
// ─────────────────────────────────────────────

// newSpillHandle 生成 <安全工具名>_<8 hex> 形式的 handle。
func newSpillHandle(toolName string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 加密随机数不可用时退化为纳秒时间（仅影响同批次的碰撞概率）
		n := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(n >> (8 * i))
		}
	}
	return sanitizeToolName(toolName) + "_" + hex.EncodeToString(b[:])
}

// sanitizeToolName 把工具名压缩成 handle 前缀：小写字母/数字/下划线保留，
// 其他字符转 '-'，最长 20 字符。MCP 工具名可能含任意字符，不能直接落成文件名。
func sanitizeToolName(name string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			sb.WriteRune(r)
		case r == '-' || r == '.' || r == '/':
			sb.WriteRune('-')
		default:
			continue
		}
		if sb.Len() >= 20 {
			break
		}
	}
	if sb.Len() == 0 {
		return "tool"
	}
	return sb.String()
}

// truncateMiddleRunes 头尾保留式截断（落盘不可用时的兜底）：多保留头部
// （错误通常在最前面），中段替换为省略标记。totalRunes 为调用方已算好的
// rune 总数，避免对超大字符串重复全量扫描。
func truncateMiddleRunes(s string, max, totalRunes int) string {
	if max <= 0 || totalRunes <= max {
		return s
	}
	headN := max * 2 / 3
	tailN := max - headN
	return headRunes(s, headN) +
		fmt.Sprintf("\n…（中间 %d 字符已截断）…\n", totalRunes-headN-tailN) +
		tailRunes(s, tailN)
}

// headRunes 返回字符串开头 n 个 rune（n<=0 返回空串，不足 n 返回全串）。
func headRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// tailRunes 返回字符串结尾 n 个 rune（n<=0 返回空串，不足 n 返回全串）。
func tailRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := len(s); i > 0; {
		i--
		if utf8.RuneStart(s[i]) {
			count++
			if count == n {
				return s[i:]
			}
		}
	}
	return s
}

// capFileBytes 把超单文件上限的内容压到 max 字节内：保留头 3/4 + 尾 1/4，
// 中间插入省略标记；切点对齐 UTF-8 rune 边界。
func capFileBytes(data []byte, max int64) []byte {
	if max <= 0 || int64(len(data)) <= max {
		return data
	}
	headLen := max * 3 / 4
	tailLen := max - headLen
	tailStart := int64(len(data)) - tailLen

	// 对齐 rune 边界：头部切点回退到 rune 起始，尾部切点前进到 rune 起始
	for headLen > 0 && !utf8.RuneStart(data[headLen]) {
		headLen--
	}
	for tailStart < int64(len(data)) && !utf8.RuneStart(data[tailStart]) {
		tailStart++
	}
	omitted := int64(len(data)) - headLen - (int64(len(data)) - tailStart)
	marker := fmt.Sprintf("\n…（文件超过单文件上限 %d MB，中间 %d 字节已省略）…\n", max>>20, omitted)
	out := make([]byte, 0, headLen+int64(len(marker))+int64(len(data))-tailStart)
	out = append(out, data[:headLen]...)
	out = append(out, marker...)
	out = append(out, data[tailStart:]...)
	return out
}

// indexRange 生成 [start, end) 的下标序列。
func indexRange(start, end int) []int {
	if end <= start {
		return nil
	}
	idx := make([]int, 0, end-start)
	for i := start; i < end; i++ {
		idx = append(idx, i)
	}
	return idx
}
