package functool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// FileToolsConfig 文件读写工具组配置（read_file / write_file / edit_file / glob / grep）。
// 让 AI 具备直接读写代码与文本文件的能力（对齐主流编码 agent 的 Read/Write/Edit 工具），
// 默认关闭：工具可读取宿主机上任意可读文件，存在提示词注入泄露敏感文件的风险，
// 故需显式开启（与 bash / file 工具一致的 opt-in 策略）。
type FileToolsConfig struct {
	Enable bool `json:"enable" mapstructure:"enable"`
	// Root 工作根目录：非空时相对路径基于它解析，且解析结果必须仍位于其内
	// （软约束，防误操作不防恶意）；留空时相对路径基于进程工作目录，不设访问边界
	Root string `json:"root" mapstructure:"root"`
}

// 文件工具的公共上限
const (
	fileReadMaxLines     = 2000 // read_file 单次最大行数
	fileReadLineMaxRunes = 2000 // read_file 单行截断
	fileEditMaxFileBytes = 32 << 20
	fileListMaxEntries   = 200 // glob 最多返回条数
	grepMaxMatches       = 300 // grep 最多返回匹配行数
	grepMaxFileBytes     = 2 << 20
	grepLineMaxRunes     = 400
	globPatternMaxLen    = 512 // glob / grep include 模式长度上限（拦截病态输入）
)

// NewFileTools 按配置构造文件读写工具组，返回 nil 表示未启用。
func NewFileTools(config FileToolsConfig) []llmtool.Tool {
	if !config.Enable {
		return nil
	}
	return []llmtool.Tool{
		NewReadFileTool(config),
		NewWriteFileTool(config),
		NewEditFileTool(config),
		NewGlobTool(config),
		NewGrepTool(config),
	}
}

// resolvePath 解析用户路径：绝对路径直接采用；相对路径基于 Root（未配置时为进程
// 工作目录）。Root 非空时校验解析结果必须仍位于 Root 内。
func (c FileToolsConfig) resolvePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("路径不能为空")
	}
	if c.Root == "" {
		return path, nil
	}
	root, err := filepath.Abs(c.Root)
	if err != nil {
		return "", fmt.Errorf("解析工作根目录失败: %w", err)
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("解析路径失败: %w", err)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径 %q 超出工作根目录 %q，已拒绝", path, c.Root)
	}
	return target, nil
}

// checkFilePath 解析路径并拦截敏感文件（与 file 工具同一策略：禁止触碰机器人数据库）。
// 除路径本身外还对符号链接解引用后的真实路径做检查：软链指向数据库可绕过
// 子树面字面匹配。
func (c FileToolsConfig) checkFilePath(path string) (string, error) {
	resolved, err := c.resolvePath(path)
	if err != nil {
		return "", err
	}
	if SensitiveHostPath(resolved) {
		return "", errors.New("禁止读写机器人数据库文件")
	}
	return resolved, nil
}

// SensitiveHostPath 判断路径是否指向机器人自身数据库（含符号链接解引用后的
// 真实路径）：数据库内含配置与凭据，禁止经 AI 工具读写或发送。
func SensitiveHostPath(path string) bool {
	const target = "aniabot.db"
	if strings.Contains(strings.ToLower(path), target) {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		if strings.Contains(strings.ToLower(resolved), target) {
			return true
		}
	}
	return false
}

// readFileData 读取文件内容并做体积/二进制预检，返回文本与是否截断。
func readFileData(path string, maxBytes int64) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s 是目录，不是文件", path)
	}
	if info.Size() > maxBytes {
		return "", fmt.Errorf("文件过大（%d 字节，上限 %d），请用 grep 定位后再用 offset/limit 分段读取", info.Size(), maxBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// 二进制预检：文件头出现 NUL 字节即按二进制拒绝（文本文件不会出现）
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return "", errors.New("疑似二进制文件，无法按文本读取")
	}
	return string(data), nil
}

// ─────────────────────────────────────────────
// read_file：按行号分段读取文本文件
// ─────────────────────────────────────────────

type ReadFileParams struct {
	Path   string `json:"path" desc:"文件路径（绝对路径，或相对工作根目录的相对路径）"`
	Offset int    `json:"offset" desc:"起始行号（从 1 开始），默认 1"`
	Limit  int    `json:"limit" desc:"最多读取的行数，默认与上限均为 2000"`
}

type ReadFileTool struct {
	llmtool.BaseTool[ReadFileParams]
	config FileToolsConfig
}

func NewReadFileTool(config FileToolsConfig) *ReadFileTool {
	return &ReadFileTool{
		BaseTool: llmtool.MakeBaseTool("read_file",
			"读取本地文本文件内容。返回带行号的文本，适合查看代码、配置、日志等。文件过大时用 offset/limit 分段读取",
			ReadFileParams{}),
		config: config,
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, params any, cbs llmtool.CallBackFuncs) (string, error) {
	p, ok := params.(*ReadFileParams)
	if !ok {
		return "", fmt.Errorf("read_file: 参数类型错误")
	}
	path, err := t.config.checkFilePath(p.Path)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}

	content, err := readFileData(path, fileEditMaxFileBytes)
	if err != nil {
		return "", fmt.Errorf("read_file: 读取失败: %w", err)
	}
	if content == "" {
		return "（空文件）", nil
	}

	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	total := len(lines)
	offset := max(p.Offset, 1)
	limit := p.Limit
	if limit <= 0 || limit > fileReadMaxLines {
		limit = fileReadMaxLines
	}
	start := offset - 1
	if start >= total {
		return fmt.Sprintf("offset=%d 超出文件范围（共 %d 行）", offset, total), nil
	}
	end := min(start+limit, total)

	var sb strings.Builder
	for i := start; i < end; i++ {
		// CRLF 文件：去掉行尾 \r 再展示，模型按所见文本构造 edit_file 的 old_string
		// （edit_file 侧做了 CRLF 回退匹配，两侧配合即可正常编辑 CRLF 文件）
		line := strings.TrimSuffix(lines[i], "\r")
		if r := []rune(line); len(r) > fileReadLineMaxRunes {
			line = string(r[:fileReadLineMaxRunes]) + "…(行截断)"
		}
		fmt.Fprintf(&sb, "%6d\t%s\n", i+1, line)
	}
	if end < total {
		fmt.Fprintf(&sb, "（显示第 %d-%d 行，共 %d 行；继续读取请传 offset=%d）\n", start+1, end, total, end+1)
	}
	return sb.String(), nil
}

// ─────────────────────────────────────────────
// write_file：整文件写入（新建或覆盖），自动创建父目录
// ─────────────────────────────────────────────

type WriteFileParams struct {
	Path    string `json:"path" desc:"目标文件路径（绝对路径，或相对工作根目录的相对路径）"`
	Content string `json:"content" desc:"要写入的内容（UTF-8 文本）；append=false 时为完整文件内容（覆盖已有文件），append=true 时为本段追加内容"`
	// Append 追加模式：分段写入长文件用——单次输出过长会因达到最大输出 Token
	// 上限被截断，超出约 200 行的内容应拆成多段，首段 append=false、后续段
	// append=true 逐段追加，替代 bash heredoc 等危险旁路
	Append bool `json:"append" desc:"true 时把 content 追加到文件末尾（文件不存在则创建），用于分段写入长文件；false/缺省为新建或整体覆盖"`
}

type WriteFileTool struct {
	llmtool.BaseTool[WriteFileParams]
	config FileToolsConfig
}

func NewWriteFileTool(config FileToolsConfig) *WriteFileTool {
	return &WriteFileTool{
		BaseTool: llmtool.MakeBaseTool("write_file",
			"把内容写入本地文本文件：append=false（默认）新建或整体覆盖，append=true 追加到文件末尾。单次输出过长会因达到最大输出 Token 上限被截断，内容超过约 200 行时分段写入：先写开头一段，再以 append=true 逐段追加。修改已有文件优先用 edit_file 做精确替换",
			WriteFileParams{}),
		config: config,
	}
}

func (t *WriteFileTool) Execute(ctx context.Context, params any, cbs llmtool.CallBackFuncs) (string, error) {
	p, ok := params.(*WriteFileParams)
	if !ok {
		return "", fmt.Errorf("write_file: 参数类型错误")
	}
	path, err := t.config.checkFilePath(p.Path)
	if err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("write_file: 创建目录失败: %w", err)
		}
	}
	if p.Append {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return "", fmt.Errorf("write_file: 打开文件失败: %w", err)
		}
		if _, err := f.WriteString(p.Content); err != nil {
			f.Close()
			return "", fmt.Errorf("write_file: 追加失败: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("write_file: 追加失败: %w", err)
		}
		return fmt.Sprintf("已追加 %d 行（%d 字节）到 %s", countLines(p.Content), len(p.Content), path), nil
	}
	_, statErr := os.Stat(path)
	existed := statErr == nil
	if err := os.WriteFile(path, []byte(p.Content), 0o644); err != nil {
		return "", fmt.Errorf("write_file: 写入失败: %w", err)
	}
	lineCount := countLines(p.Content)
	tag := "新建文件"
	if existed {
		tag = "覆盖已有文件"
	}
	return fmt.Sprintf("已写入 %s（%d 行，%d 字节，%s）", path, lineCount, len(p.Content), tag), nil
}

// countLines 统计文本行数：按换行符计，结尾换行不算新起一行，空文本为 0 行。
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// ─────────────────────────────────────────────
// edit_file：精确字符串替换编辑（对齐主流编码 agent 的 Edit 工具）
// ─────────────────────────────────────────────

type EditFileParams struct {
	Path       string `json:"path" desc:"要编辑的文件路径"`
	OldString  string `json:"old_string" desc:"要被替换的原文，必须与文件内容逐字符一致（含缩进与换行）；应包含足够上下文以保证在文件中唯一"`
	NewString  string `json:"new_string" desc:"替换后的新文本"`
	ReplaceAll bool   `json:"replace_all" desc:"true 时替换文件中所有出现；默认要求 old_string 唯一"`
}

type EditFileTool struct {
	llmtool.BaseTool[EditFileParams]
	config FileToolsConfig
}

func NewEditFileTool(config FileToolsConfig) *EditFileTool {
	return &EditFileTool{
		BaseTool: llmtool.MakeBaseTool("edit_file",
			"对本地文本文件做精确替换编辑：old_string 必须与文件内容逐字符一致且唯一，替换为 new_string。修改前建议先 read_file 确认原文（含缩进）。适合改代码、配置的小范围修改",
			EditFileParams{}),
		config: config,
	}
}

func (t *EditFileTool) Execute(ctx context.Context, params any, cbs llmtool.CallBackFuncs) (string, error) {
	p, ok := params.(*EditFileParams)
	if !ok {
		return "", fmt.Errorf("edit_file: 参数类型错误")
	}
	if p.OldString == "" {
		return "", fmt.Errorf("edit_file: old_string 不能为空")
	}
	if p.OldString == p.NewString {
		return "", fmt.Errorf("edit_file: old_string 与 new_string 相同，无需编辑")
	}
	path, err := t.config.checkFilePath(p.Path)
	if err != nil {
		return "", fmt.Errorf("edit_file: %w", err)
	}

	content, err := readFileData(path, fileEditMaxFileBytes)
	if err != nil {
		return "", fmt.Errorf("edit_file: 读取失败: %w", err)
	}

	// CRLF 文件回退匹配：read_file 展示时去掉了行尾 \r，模型给出的 old_string 多半
	// 带 \n；直接匹配失败且文件含 CRLF 时，按 CRLF 变体重试，替换文本同样归一为
	// CRLF，保持文件换行风格一致（替换仍在原文上进行，不重写其他行的换行符）
	count := strings.Count(content, p.OldString)
	old, replacement := p.OldString, p.NewString
	if count == 0 && strings.Contains(content, "\r\n") && strings.Contains(p.OldString, "\n") {
		if crlf := strings.ReplaceAll(p.OldString, "\n", "\r\n"); crlf != p.OldString {
			if c := strings.Count(content, crlf); c > 0 {
				count, old = c, crlf
				// 先归一到 LF 再转 CRLF，避免 new_string 本身含 \r\n 时被翻倍
				replacement = strings.ReplaceAll(strings.ReplaceAll(p.NewString, "\r\n", "\n"), "\n", "\r\n")
			}
		}
	}
	if count == 0 {
		return "", fmt.Errorf("edit_file: old_string 未在 %s 中找到。请先 read_file 确认原文（空白、缩进、换行必须逐字符一致），再重试", path)
	}
	if count > 1 && !p.ReplaceAll {
		return "", fmt.Errorf("edit_file: old_string 在文件中出现 %d 次，不唯一。请在 old_string 前后包含更多上下文使其唯一，或确认要全部替换时设 replace_all=true", count)
	}

	updated := strings.ReplaceAll(content, old, replacement)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("edit_file: 写回失败: %w", err)
	}

	newLineCount := countLines(updated)
	snippet := p.NewString
	if r := []rune(snippet); len(r) > 600 {
		snippet = string(r[:600]) + "…"
	}
	return fmt.Sprintf("已替换 %d 处，文件现在共 %d 行。替换后内容：\n%s", count, newLineCount, snippet), nil
}

// ─────────────────────────────────────────────
// glob：按模式匹配文件路径（支持 ** 跨目录）
// ─────────────────────────────────────────────

type GlobParams struct {
	Pattern string `json:"pattern" desc:"glob 模式，如 *.go、docs/**/*.md、**/*.yaml；** 可匹配任意层级目录"`
	Path    string `json:"path" desc:"搜索根目录，默认为工作根目录（未配置时为进程工作目录）"`
}

type GlobTool struct {
	llmtool.BaseTool[GlobParams]
	config FileToolsConfig
}

func NewGlobTool(config FileToolsConfig) *GlobTool {
	return &GlobTool{
		BaseTool: llmtool.MakeBaseTool("glob",
			"按 glob 模式查找文件路径（支持 ** 跨目录匹配），结果按修改时间倒序。找文件、确认文件是否存在、列出某类文件时使用",
			GlobParams{}),
		config: config,
	}
}

func (t *GlobTool) Execute(ctx context.Context, params any, cbs llmtool.CallBackFuncs) (string, error) {
	gp, ok := params.(*GlobParams)
	if !ok {
		return "", fmt.Errorf("glob: 参数类型错误")
	}
	if strings.TrimSpace(gp.Pattern) == "" {
		return "", fmt.Errorf("glob: pattern 不能为空")
	}
	if len(gp.Pattern) > globPatternMaxLen {
		return "", fmt.Errorf("glob: pattern 过长（%d 字符，上限 %d）", len(gp.Pattern), globPatternMaxLen)
	}
	root := gp.Path
	if strings.TrimSpace(root) == "" {
		root = t.config.Root
	}
	rootPath, err := t.config.checkFilePath(root)
	if err != nil {
		return "", fmt.Errorf("glob: %w", err)
	}

	var matches []string
	err = filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单个条目失败不中断遍历
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(rootPath, path)
		if relErr != nil {
			return nil
		}
		if matchGlob(gp.Pattern, filepath.ToSlash(rel)) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("glob: 遍历失败: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Sprintf("未找到匹配 %q 的文件", gp.Pattern), nil
	}

	// 按修改时间倒序（最新改动的排前面，方便定位最近编辑的文件）
	type fileMod struct {
		path string
		mod  int64
	}
	entries := make([]fileMod, 0, len(matches))
	for _, m := range matches {
		var mod int64
		if info, statErr := os.Stat(m); statErr == nil {
			mod = info.ModTime().UnixNano()
		}
		entries = append(entries, fileMod{m, mod})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod > entries[j].mod })

	truncated := len(entries) > fileListMaxEntries
	var sb strings.Builder
	fmt.Fprintf(&sb, "共 %d 个文件匹配 %q", len(entries), gp.Pattern)
	if truncated {
		fmt.Fprintf(&sb, "（按修改时间倒序，仅显示前 %d 个）", fileListMaxEntries)
	}
	sb.WriteString("：\n")
	for _, e := range entries[:min(fileListMaxEntries, len(entries))] {
		sb.WriteString(e.path)
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// matchGlob 报告 name（相对路径，'/' 分隔）是否匹配 pattern（支持 **、*、?）。
// 与标准库 filepath.Glob 不同：支持 ** 跨任意层级目录匹配。
func matchGlob(pattern, name string) bool {
	return globSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// globSegs 逐段匹配：pattern 段与 name 段一一对应，** 段可吞并任意数量（含 0）的 name 段。
// ** 与后续段存在大量重叠子问题，带 memoization 避免指数回溯（模式由模型生成，需防病态输入）。
func globSegs(pattern, name []string) bool {
	memo := make(map[[2]int]bool, len(pattern)*(len(name)+1))
	var match func(pi, ni int) bool
	match = func(pi, ni int) bool {
		if pi == len(pattern) {
			return ni == len(name)
		}
		key := [2]int{pi, ni}
		if res, ok := memo[key]; ok {
			return res
		}
		var res bool
		if pattern[pi] == "**" {
			// ** 吞并 0 到 n 个 name 段
			for i := ni; i <= len(name) && !res; i++ {
				res = match(pi+1, i)
			}
		} else if ni < len(name) && globSegMatch(pattern[pi], name[ni]) {
			res = match(pi+1, ni+1)
		}
		memo[key] = res
		return res
	}
	return match(0, 0)
}

// globSegMatch 单段匹配：仅 *（任意字符序列）与 ?（单个字符）两个通配符。
// 迭代 DP（cur/next 行滚动表示 pattern[i:] 是否匹配 name[j:]），代价
// O(len(pattern)×len(name))，避免递归回溯在 * 密集的病态模式下指数爆炸。
func globSegMatch(pattern, name string) bool {
	n := len(name)
	next := make([]bool, n+1) // pattern[i+1:] 的匹配结果行
	next[n] = true            // 空模式只匹配空名
	cur := make([]bool, n+1)
	for i := len(pattern) - 1; i >= 0; i-- {
		switch c := pattern[i]; c {
		case '*':
			// 吞并 0 个字符走 next[j]，吞并 ≥1 个走同行右侧 cur[j+1]；j 需从大到小
			for j := n; j >= 0; j-- {
				cur[j] = next[j] || (j < n && cur[j+1])
			}
		case '?':
			for j := n; j >= 0; j-- {
				cur[j] = j < n && next[j+1]
			}
		default:
			for j := n; j >= 0; j-- {
				cur[j] = j < n && name[j] == c && next[j+1]
			}
		}
		cur, next = next, cur
	}
	return next[0]
}

// ─────────────────────────────────────────────
// grep：正则搜索文件内容
// ─────────────────────────────────────────────

type GrepParams struct {
	Pattern string `json:"pattern" desc:"正则表达式（Go 语法），如 TODO、func \\(\\w+ \\*?[A-Za-z]+\\) Execute"`
	Path    string `json:"path" desc:"要搜索的文件或目录，默认为工作根目录（未配置时为进程工作目录）"`
	Include string `json:"include" desc:"文件名过滤 glob，如 *.go、*.{ts,tsx} 仅支持单一模式；为空时搜索所有文本文件"`
}

type GrepTool struct {
	llmtool.BaseTool[GrepParams]
	config FileToolsConfig
}

func NewGrepTool(config FileToolsConfig) *GrepTool {
	return &GrepTool{
		BaseTool: llmtool.MakeBaseTool("grep",
			"用正则表达式在文件内容中搜索，输出 文件:行号: 匹配行。查代码符号、引用、关键字定位时使用；配合 include 参数按文件类型过滤",
			GrepParams{}),
		config: config,
	}
}

// grepSkipDirs 遍历时跳过的目录名（依赖目录/版本库，内容庞大且无搜索价值）
var grepSkipDirs = map[string]struct{}{
	".git": {}, "node_modules": {}, "vendor": {}, "__pycache__": {}, ".venv": {}, "venv": {},
}

func (t *GrepTool) Execute(ctx context.Context, params any, cbs llmtool.CallBackFuncs) (string, error) {
	gp, ok := params.(*GrepParams)
	if !ok {
		return "", fmt.Errorf("grep: 参数类型错误")
	}
	if strings.TrimSpace(gp.Pattern) == "" {
		return "", fmt.Errorf("grep: pattern 不能为空")
	}
	re, err := regexp.Compile(gp.Pattern)
	if err != nil {
		return "", fmt.Errorf("grep: 正则编译失败: %w", err)
	}
	var include *globMatcher
	if strings.TrimSpace(gp.Include) != "" {
		if len(gp.Include) > globPatternMaxLen {
			return "", fmt.Errorf("grep: include 过长（%d 字符，上限 %d）", len(gp.Include), globPatternMaxLen)
		}
		include = newGlobMatcher(gp.Include)
	}

	root := gp.Path
	if strings.TrimSpace(root) == "" {
		root = t.config.Root
	}
	rootPath, err := t.config.checkFilePath(root)
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}
	info, err := os.Stat(rootPath)
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}

	var sb strings.Builder
	matchTotal := 0
	fileTotal := 0
	truncated := false

	searchFile := func(path string) {
		if include != nil && !include.match(filepath.Base(path)) {
			return
		}
		fi, statErr := os.Stat(path)
		if statErr != nil || fi.IsDir() || fi.Size() > grepMaxFileBytes {
			return
		}
		content, readErr := readFileData(path, grepMaxFileBytes)
		if readErr != nil {
			return // 二进制/过大/无权限：静默跳过
		}
		fileMatched := false
		for i, line := range strings.Split(content, "\n") {
			if !re.MatchString(line) {
				continue
			}
			fileMatched = true
			if matchTotal < grepMaxMatches {
				text := strings.TrimRight(line, "\r")
				if r := []rune(text); len(r) > grepLineMaxRunes {
					text = string(r[:grepLineMaxRunes]) + "…"
				}
				fmt.Fprintf(&sb, "%s:%d: %s\n", path, i+1, text)
			}
			matchTotal++
			if matchTotal >= grepMaxMatches {
				truncated = true
			}
		}
		if fileMatched {
			fileTotal++
		}
	}

	if info.IsDir() {
		filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if d.IsDir() {
				if path != rootPath {
					if _, skip := grepSkipDirs[d.Name()]; skip {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if truncated {
				return filepath.SkipAll
			}
			searchFile(path)
			return nil
		})
	} else {
		searchFile(rootPath)
	}

	if matchTotal == 0 {
		return fmt.Sprintf("未找到匹配 %q 的内容", gp.Pattern), nil
	}
	if truncated {
		fmt.Fprintf(&sb, "（匹配数超过 %d，涉及 %d 个文件，结果已截断；建议收窄 pattern 或 include 后重试）", grepMaxMatches, fileTotal)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// globMatcher 编译一次、可多次匹配的文件名 glob（支持 * ?，不支持 **——文件名无目录层级）
type globMatcher struct {
	segs []byte
}

func newGlobMatcher(pattern string) *globMatcher {
	return &globMatcher{segs: []byte(pattern)}
}

func (g *globMatcher) match(name string) bool {
	return globSegMatch(string(g.segs), name)
}
