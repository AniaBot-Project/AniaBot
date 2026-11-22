package pluginaichat

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// testLimiter 构造落在临时目录的治理器（不启动后台清理，测试直接调用 cleanup）。
func testLimiter(t *testing.T, cfg toolResultConfig) *toolResultLimiter {
	t.Helper()
	cfg.Dir = t.TempDir()
	if cfg.MaxInlineRunes == 0 {
		cfg.MaxInlineRunes = 100
	}
	if cfg.PreviewRunes == 0 {
		cfg.PreviewRunes = 40
	}
	if cfg.MaxFileMB == 0 {
		cfg.MaxFileMB = 1
	}
	return newToolResultLimiter(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

var spillHandlePattern = regexp.MustCompile(`handle=([a-z0-9_-]+)`)

// TestToolResultLimiterPassThrough 未超过内联上限的结果原样返回，不落盘。
func TestToolResultLimiterPassThrough(t *testing.T) {
	l := testLimiter(t, toolResultConfig{Enable: true, MaxInlineRunes: 100, PreviewRunes: 40})
	if got := l.Limit("g:1", "bash", "short output", false); got != "short output" {
		t.Fatalf("短结果应原样返回: %q", got)
	}
}

// TestToolResultLimiterSpillAndRead 超限成功结果落盘：回填预览+handle，
// 可按 offset/limit、from_end、pattern 读回完整内容。
func TestToolResultLimiterSpillAndRead(t *testing.T) {
	l := testLimiter(t, toolResultConfig{Enable: true, MaxInlineRunes: 100, PreviewRunes: 40})
	head := strings.Repeat("A", 150)
	tail := strings.Repeat("B", 150)
	content := head + "\n" + tail

	out := l.Limit("g:1", "bash", content, false)
	if !strings.Contains(out, "【工具结果已落盘】") {
		t.Fatalf("超限结果应落盘提示: %q", out)
	}
	if !strings.Contains(out, "read_tool_output") {
		t.Fatalf("提示应给出读取方式: %q", out)
	}
	m := spillHandlePattern.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("提示中应包含 handle: %q", out)
	}
	handle := m[1]
	if !spillHandleRe.MatchString(handle) {
		t.Fatalf("handle 格式非法: %q", handle)
	}
	if strings.Contains(out, strings.Repeat("A", 100)) {
		t.Fatalf("预览不应包含完整头部内容: %d runes", len([]rune(out)))
	}

	// 默认分页：两行内容
	got, err := l.Read("g:1", handle, 0, 0, false, "")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !strings.Contains(got, "共 2 行") || !strings.Contains(got, head) || !strings.Contains(got, tail) {
		t.Fatalf("默认读取应返回完整内容: %q", got)
	}
	// offset/limit 分页
	got, err = l.Read("g:1", handle, 2, 1, false, "")
	if err != nil {
		t.Fatalf("分页读取失败: %v", err)
	}
	if strings.Contains(got, head) || !strings.Contains(got, tail) {
		t.Fatalf("offset=2 应只返回第二行: %q", got)
	}
	// from_end 读末尾一行
	got, err = l.Read("g:1", handle, 0, 1, true, "")
	if err != nil {
		t.Fatalf("from_end 读取失败: %v", err)
	}
	if !strings.Contains(got, "from_end=true") || !strings.Contains(got, tail) {
		t.Fatalf("from_end 应返回末尾行: %q", got)
	}
	// pattern 过滤
	got, err = l.Read("g:1", handle, 0, 200, false, "bbb")
	if err != nil {
		t.Fatalf("pattern 读取失败: %v", err)
	}
	if !strings.Contains(got, `pattern="bbb"`) || !strings.Contains(got, tail) || strings.Contains(got, head) {
		t.Fatalf("pattern 应只返回命中行: %q", got)
	}
	// 未命中
	got, err = l.Read("g:1", handle, 0, 10, false, "no-such-text")
	if err != nil || !strings.Contains(got, "无匹配") {
		t.Fatalf("未命中应返回提示: got=%q err=%v", got, err)
	}
}

// TestToolResultLimiterDisabled 关闭落盘时超限结果只做头尾截断。
func TestToolResultLimiterDisabled(t *testing.T) {
	l := testLimiter(t, toolResultConfig{Enable: false, MaxInlineRunes: 100, PreviewRunes: 40})
	out := l.Limit("g:1", "bash", strings.Repeat("x", 300), false)
	if strings.Contains(out, "落盘") {
		t.Fatalf("关闭落盘时不应出现落盘提示: %q", out)
	}
	if !strings.Contains(out, "已截断") {
		t.Fatalf("关闭落盘时应头尾截断: %q", out)
	}
	if r := []rune(out); len(r) > 200 {
		t.Fatalf("截断后应明显变短: %d runes", len(r))
	}
}

// TestToolResultLimiterErrorTruncates 错误结果只截断、不落盘（错误必须当轮可见）。
func TestToolResultLimiterErrorTruncates(t *testing.T) {
	l := testLimiter(t, toolResultConfig{Enable: true, MaxInlineRunes: 100, PreviewRunes: 40})
	out := l.Limit("g:1", "bash", "E"+strings.Repeat("x", 300), true)
	if strings.Contains(out, "落盘") {
		t.Fatalf("错误结果不应落盘: %q", out)
	}
	if !strings.Contains(out, "已截断") || !strings.HasPrefix(out, "E") {
		t.Fatalf("错误结果应保留头部并截断: %q", out)
	}
}

// TestToolResultLimiterSkipsReadTool read_tool_output 自身结果不落盘（防递归）。
func TestToolResultLimiterSkipsReadTool(t *testing.T) {
	l := testLimiter(t, toolResultConfig{Enable: true, MaxInlineRunes: 100, PreviewRunes: 40})
	big := strings.Repeat("r", 500)
	if got := l.Limit("g:1", "read_tool_output", big, false); got != big {
		t.Fatalf("read_tool_output 结果应豁免: %d runes", len([]rune(got)))
	}
}

// TestToolResultLimiterReadToolExemptionBound 豁免按"名字 + 不超过读回硬上限"
// 判定：真实读回结果（≤ spillReadMaxRunes）豁免；同名工具返回超大结果时仍会被
// 统一治理，不能借名字绕过长度限制。
func TestToolResultLimiterReadToolExemptionBound(t *testing.T) {
	l := testLimiter(t, toolResultConfig{Enable: true, MaxInlineRunes: 100, PreviewRunes: 40})
	real := strings.Repeat("r", spillReadMaxRunes)
	if got := l.Limit("g:1", "read_tool_output", real, false); got != real {
		t.Fatalf("真实读回结果应豁免: %d runes", len([]rune(got)))
	}
	fake := strings.Repeat("f", spillReadMaxRunes+1000)
	out := l.Limit("g:1", "read_tool_output", fake, false)
	if out == fake {
		t.Fatal("超过读回硬上限的同名结果不应豁免")
	}
	if !strings.Contains(out, "【工具结果已落盘】") {
		t.Fatalf("超限结果应落盘: %q", out)
	}
}

// TestToolResultLimiterSessionIsolation handle 只能在所属会话目录读回。
func TestToolResultLimiterSessionIsolation(t *testing.T) {
	l := testLimiter(t, toolResultConfig{Enable: true, MaxInlineRunes: 100, PreviewRunes: 40})
	out := l.Limit("g:1", "bash", strings.Repeat("x", 300), false)
	m := spillHandlePattern.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("缺少 handle: %q", out)
	}
	if _, err := l.Read("g:2", m[1], 0, 10, false, ""); err == nil {
		t.Fatal("跨会话读取应失败")
	}
	if _, err := l.Read("g:1", "../evil", 0, 10, false, ""); err == nil {
		t.Fatal("非法 handle 应失败")
	}
}

// TestSpillStoreCleanup 过期文件与超总量文件会被后台清理删除。
func TestSpillStoreCleanup(t *testing.T) {
	dir := t.TempDir()
	s := newSpillStore(toolResultConfig{
		Enable:         true,
		Dir:            dir,
		MaxFileMB:      1,
		RetentionHours: 1,
		MaxTotalMB:     1,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if s == nil {
		t.Fatal("store 初始化失败")
	}
	if _, _, err := s.write("g:expired", "bash", strings.Repeat("e", 200)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 把文件时间改到 2 小时前，超过 1 小时保留期
	expiredDir := s.sessionDir("g:expired")
	entries, _ := os.ReadDir(expiredDir)
	if len(entries) != 1 {
		t.Fatalf("期望 1 个文件, got %d", len(entries))
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(expiredDir, entries[0].Name()), old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	// 再写两份 700KB 文件，让总量超过 1MB 上限
	for i := 0; i < 2; i++ {
		if _, _, err := s.write("g:big", "bash", strings.Repeat("b", 700*1024)); err != nil {
			t.Fatalf("写入大文件失败: %v", err)
		}
	}
	s.cleanup()

	if _, err := os.Stat(expiredDir); !os.IsNotExist(err) {
		t.Fatalf("过期会话目录应被删除: %v", err)
	}
	var total int64
	_ = filepath.WalkDir(s.baseDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if total > s.maxTotalBytes {
		t.Fatalf("清理后总量仍超上限: %d > %d", total, s.maxTotalBytes)
	}
}

// TestCapFileBytes 单文件超限时保留头尾且不切断 UTF-8 字符。
func TestCapFileBytes(t *testing.T) {
	data := []byte(strings.Repeat("你好", 200)) // 600 字节，1200 字节内容
	out := capFileBytes(data, 100)
	if int64(len(out)) > 100+1024 { // 100 字节 + 省略标记
		t.Fatalf("封顶后仍过大: %d", len(out))
	}
	if !utf8.Valid(out) {
		t.Fatal("封顶后不是合法 UTF-8")
	}
	if !strings.Contains(string(out), "省略") {
		t.Fatalf("应包含省略标记: %q", out)
	}
	if !strings.HasPrefix(string(out), "你好") || !strings.HasSuffix(string(out), "你好") {
		t.Fatalf("应保留头尾: %q", out[:min(len(out), 40)])
	}
}

// TestTruncateMiddleRunes 头尾保留比例与 rune 边界。
func TestTruncateMiddleRunes(t *testing.T) {
	s := strings.Repeat("a", 120) + "我们" + strings.Repeat("b", 120)
	total := utf8.RuneCountInString(s)
	got := truncateMiddleRunes(s, 40, total)
	if !strings.Contains(got, "已截断") {
		t.Fatalf("应包含截断标记: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("截断结果不是合法 UTF-8")
	}
	if !strings.HasPrefix(got, "aaaa") || !strings.HasSuffix(got, "bbbb") {
		t.Fatalf("应保留头尾: %q", got)
	}
	if got := truncateMiddleRunes("short", 40, 5); got != "short" {
		t.Fatalf("未超限不应截断: %q", got)
	}
}

// TestToolResultLimiterCappedFileNotice 落盘文件超过单文件上限时，提示需明确
// 告知文件已裁剪，且 handle 仍可读。
func TestToolResultLimiterCappedFileNotice(t *testing.T) {
	l := testLimiter(t, toolResultConfig{
		Enable: true, MaxInlineRunes: 100, PreviewRunes: 40, MaxFileMB: 1,
	})
	out := l.Limit("g:1", "bash", strings.Repeat("c", 2<<20), false)
	if !strings.Contains(out, "单文件上限") {
		t.Fatalf("超单文件上限时应提示已裁剪: %q", out)
	}
	m := spillHandlePattern.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("缺少 handle: %q", out)
	}
	got, err := l.Read("g:1", m[1], 1, 1, false, "")
	if err != nil || !strings.Contains(got, "ccccc") {
		t.Fatalf("裁剪后的文件仍应可读: got=%q err=%v", got, err)
	}
}

// TestLimiterSanitizeConfig 非法/越界配置被收敛到安全值，预览不能达到内联上限。
func TestLimiterSanitizeConfig(t *testing.T) {
	l := newToolResultLimiter(toolResultConfig{
		Enable:         false,
		MaxInlineRunes: -5,
		PreviewRunes:   99999,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if l.InlineRunes() != spillDefaultInlineRunes {
		t.Fatalf("内联上限应回落默认值: %d", l.InlineRunes())
	}
	if l.PreviewRunes() >= l.InlineRunes() {
		t.Fatalf("预览必须小于内联上限: preview=%d inline=%d", l.PreviewRunes(), l.InlineRunes())
	}
}

// TestAttachToolResultLimit 回调闭包绑定会话：落盘结果只能在同一会话读回。
func TestAttachToolResultLimit(t *testing.T) {
	p := &AIChatPlugin{toolResultLimiter: testLimiter(t, toolResultConfig{
		Enable: true, MaxInlineRunes: 100, PreviewRunes: 40,
	})}
	var cbs llmtool.CallBackFuncs
	p.attachToolResultLimit(&cbs, "g:attach")
	if cbs.LimitToolResult == nil || cbs.ReadToolOutput == nil {
		t.Fatal("attach 后应挂载两个回调")
	}
	out := cbs.LimitToolResult("bash", strings.Repeat("z", 300), false)
	m := spillHandlePattern.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("应落盘并给出 handle: %q", out)
	}
	got, err := cbs.ReadToolOutput(m[1], 1, 10, false, "")
	if err != nil || !strings.Contains(got, "zzz") {
		t.Fatalf("同会话应能读回: got=%q err=%v", got, err)
	}

	other := &AIChatPlugin{toolResultLimiter: p.toolResultLimiter}
	var otherCbs llmtool.CallBackFuncs
	other.attachToolResultLimit(&otherCbs, "g:other")
	if _, err := otherCbs.ReadToolOutput(m[1], 1, 10, false, ""); err == nil {
		t.Fatal("其他会话不应能读回该 handle")
	}

	// nil limiter 时 attach 不应 panic，也不应挂载回调
	empty := &AIChatPlugin{}
	var emptyCbs llmtool.CallBackFuncs
	empty.attachToolResultLimit(&emptyCbs, "g:nil")
	if emptyCbs.LimitToolResult != nil || emptyCbs.ReadToolOutput != nil {
		t.Fatal("nil limiter 不应挂载回调")
	}
}
