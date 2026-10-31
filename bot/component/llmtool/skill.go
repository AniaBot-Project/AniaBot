package llmtool

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// SkillMeta 是从 SKILL.md frontmatter 中解析的元数据
type SkillMeta struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Skill 代表一个已加载的 skill
type Skill struct {
	Meta       SkillMeta
	Content    string            // SKILL.md 的完整内容（含 frontmatter）
	Path       string            // SKILL.md 文件路径
	DirPath    string            // skill 所在目录路径
	References map[string]string // 引用文档：文件名 -> 内容（如 reference.md）
	ExtraFiles map[string]string // 附带文件：文件名 -> 绝对路径（如 script.sh）
	// Builtin 表示内置技能（随二进制发布，物化在 <skillsDir>/.builtin）。
	// 内置技能不可删除、不可禁用、不受 skills 白名单限制。
	Builtin bool
}

// builtinSkillsDirName 内置技能在 skillsDir 下的物化目录（隐藏目录，
// 不作为外置技能扫描），其脚本需要真实文件路径供 bash 执行。
const builtinSkillsDirName = ".builtin"

// SkillManager 管理所有可用的 Skill
type SkillManager struct {
	mu     sync.RWMutex
	skills map[string]*Skill // key: skill name
	// dir/names 记录最近一次加载的目录与白名单，供 Refresh 原地重载
	dir   string
	names []string

	// disabled 记录被禁用的外置 skill 名称：禁用后不出现在 available_skills
	// （描述不进入上下文），skill_read 也会拒绝读取；内置 skill 不允许禁用。
	disabled map[string]struct{}

	// builtinFS/builtinRoot 内置技能来源（embed.FS）；加载时物化到
	// <skillsDir>/.builtin，保证附带脚本有可执行的绝对路径。
	builtinFS   fs.FS
	builtinRoot string
}

// NewSkillManager 创建一个新的 SkillManager
func NewSkillManager() *SkillManager {
	return &SkillManager{
		skills:   make(map[string]*Skill),
		disabled: make(map[string]struct{}),
	}
}

// EnableBuiltinSkills 设置内置技能来源（随二进制 embed 的 FS）。
// 必须在 LoadFromDir* 之前调用；若已从目录加载过则立即重载以纳入内置技能。
func (m *SkillManager) EnableBuiltinSkills(fsys fs.FS, root string) {
	m.mu.Lock()
	m.builtinFS = fsys
	m.builtinRoot = root
	dir, names := m.dir, m.names
	m.mu.Unlock()
	if dir != "" {
		if err := m.Reload(dir, names); err != nil {
			log.Printf("加载内置 skill 失败: %v", err)
		}
	}
}

// LoadFromDir 从指定目录扫描并加载所有 skill
// 支持两种目录结构：
//   - skillsDir/skill-name/SKILL.md （可包含附属文件如 reference.md、script.sh 等）
//   - skillsDir/SKILL.md （单文件模式）
func (m *SkillManager) LoadFromDir(skillsDir string) error {
	return m.LoadFromDirWithFilter(skillsDir, nil)
}

// LoadFromDirWithFilter 从指定目录加载 skill，只加载 names 中指定的 skill
// names 为空时等同于 LoadFromDir（加载全部）
func (m *SkillManager) LoadFromDirWithFilter(skillsDir string, names []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadFromDirLocked(skillsDir, names); err != nil {
		return err
	}
	m.dir = skillsDir
	m.names = names
	return nil
}

// loadFromDirLocked 是 LoadFromDirWithFilter 的无锁内部实现。
// 先加载 skillsDir 下的外置技能（受白名单限制），再加载内置技能
// （不受白名单限制、同名时覆盖外置技能）。
func (m *SkillManager) loadFromDirLocked(skillsDir string, names []string) error {
	var filter map[string]struct{}
	if len(names) > 0 {
		filter = make(map[string]struct{}, len(names))
		for _, n := range names {
			filter[n] = struct{}{}
		}
	}

	loaded := make(map[string]*Skill)

	// 外置技能：skillsDir 首次运行时可能不存在，视为空目录而不是错误
	// （内置技能仍会照常物化并加载）。
	entries, err := os.ReadDir(skillsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("读取 skills 目录失败: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// 隐藏目录（如 .builtin、上传临时目录）不是外置技能
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			// 子目录模式：加载 SKILL.md 和附属文件
			skillDir := filepath.Join(skillsDir, entry.Name())
			skillPath := filepath.Join(skillDir, "SKILL.md")

			if _, err := os.Stat(skillPath); os.IsNotExist(err) {
				continue
			}

			skill, err := loadSkillFromDir(skillPath, skillDir)
			if err != nil {
				return fmt.Errorf("加载 skill 失败 [%s]: %w", skillPath, err)
			}
			if filter == nil {
				loaded[skill.Meta.Name] = skill
			} else if _, ok := filter[skill.Meta.Name]; ok {
				loaded[skill.Meta.Name] = skill
			}
		} else if strings.ToUpper(entry.Name()) == "SKILL.MD" {
			// 单文件模式：skillsDir/SKILL.md
			skillPath := filepath.Join(skillsDir, entry.Name())

			skill, err := loadSkillFromFile(skillPath)
			if err != nil {
				return fmt.Errorf("加载 skill 失败 [%s]: %w", skillPath, err)
			}

			if filter == nil {
				loaded[skill.Meta.Name] = skill
			} else if _, ok := filter[skill.Meta.Name]; ok {
				loaded[skill.Meta.Name] = skill
			}
		}
	}

	// 内置技能：物化到磁盘后按普通技能加载，脚本才有可执行的绝对路径
	if err := m.loadBuiltinSkillsLocked(skillsDir, loaded); err != nil {
		return err
	}

	m.skills = loaded
	return nil
}

// loadBuiltinSkillsLocked 将内置技能释放到磁盘并加载进 loaded。
// 物化失败（如 skillsDir 只读）时回退到系统临时目录，保证内置技能仍可用。
func (m *SkillManager) loadBuiltinSkillsLocked(skillsDir string, loaded map[string]*Skill) error {
	if m.builtinFS == nil || m.builtinRoot == "" {
		return nil
	}
	target := filepath.Join(skillsDir, builtinSkillsDirName)
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("创建 skills 目录失败: %w", err)
	}
	if err := materializeBuiltinSkills(m.builtinFS, m.builtinRoot, target); err != nil {
		tmp, terr := os.MkdirTemp("", "aniabot-builtin-skills-*")
		if terr != nil {
			return fmt.Errorf("释放内置 skill 失败: %w", err)
		}
		if terr := materializeBuiltinSkills(m.builtinFS, m.builtinRoot, tmp); terr != nil {
			return fmt.Errorf("释放内置 skill 失败: %w", terr)
		}
		target = tmp
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		return fmt.Errorf("读取内置 skill 目录失败: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillDir := filepath.Join(target, entry.Name())
		skillPath := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue
		}
		skill, err := loadSkillFromDir(skillPath, skillDir)
		if err != nil {
			return fmt.Errorf("加载内置 skill 失败 [%s]: %w", skillPath, err)
		}
		skill.Builtin = true
		if _, exists := loaded[skill.Meta.Name]; exists {
			log.Printf("内置 skill %s 覆盖了同名外置 skill", skill.Meta.Name)
		}
		loaded[skill.Meta.Name] = skill
	}
	return nil
}

// materializeBuiltinSkills 把内置 FS 中的 root 目录完整写到 target（先清空）。
// 脚本文件加可执行位，方便直接 `sh 路径` / `python 路径` 调用。
func materializeBuiltinSkills(fsys fs.FS, root, target string) error {
	root = strings.TrimSuffix(filepath.ToSlash(root), "/")
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	return fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(p))
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("内置 skill 包含非法路径: %s", p)
		}
		dst := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		switch strings.ToLower(filepath.Ext(p)) {
		case ".sh", ".py", ".ps1":
			mode = 0o755
		}
		return os.WriteFile(dst, data, mode)
	})
}

// newLoader 创建一个继承内置技能来源的加载器（供 Reload/Refresh 原子替换）。
func (m *SkillManager) newLoader() *SkillManager {
	tmp := NewSkillManager()
	m.mu.RLock()
	tmp.builtinFS, tmp.builtinRoot = m.builtinFS, m.builtinRoot
	m.mu.RUnlock()
	return tmp
}

// Reload 重新从磁盘加载 skill 并原子替换当前注册表（用于面板上传/删除后热更新）。
// 内置技能与禁用状态保持不变。
func (m *SkillManager) Reload(skillsDir string, names []string) error {
	tmp := m.newLoader()
	if err := tmp.loadFromDirLocked(skillsDir, names); err != nil {
		return err
	}
	m.mu.Lock()
	m.skills = tmp.skills
	m.dir = skillsDir
	m.names = names
	m.mu.Unlock()
	return nil
}

// Refresh 按最近一次加载的目录与白名单重新从磁盘加载全部 skill。
// 用于 AI/用户直接编辑本地 skill 文件后刷新缓存（面板/工具的安装删除
// 已自动重载，此接口面向「绕过管理器直接改文件」的场景）。
// 返回重载后的 skill 数量；从未从目录加载过时返回错误。
func (m *SkillManager) Refresh() (int, error) {
	m.mu.RLock()
	dir, names := m.dir, m.names
	m.mu.RUnlock()
	if dir == "" {
		return 0, fmt.Errorf("skill 未从目录加载，无法刷新")
	}
	if err := m.Reload(dir, names); err != nil {
		return 0, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.skills), nil
}

// Register 手动注册一个 Skill（直接传入路径）
func (m *SkillManager) Register(skillPath string) error {
	skill, err := loadSkillFromFile(skillPath)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.skills[skill.Meta.Name] = skill
	m.mu.Unlock()
	return nil
}

// Get 获取指定名称的 skill（含已禁用与内置技能，供面板与内部管理使用；
// AI 侧读取前应先用 IsDisabled 判断）。
func (m *SkillManager) Get(name string) (*Skill, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.skills[name]
	return s, ok
}

// IsDisabled 判断 skill 是否被禁用：禁用后描述不进入上下文，
// skill_read 也会拒绝读取（内置 skill 永远可用）。
func (m *SkillManager) IsDisabled(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.disabled[name]
	return ok
}

// SetDisabled 设置被禁用的外置 skill 名称集合（内置 skill 忽略并记录日志）。
// 未知名称会保留：skill 可能因白名单暂未加载，重新加载后禁用依然生效。
func (m *SkillManager) SetDisabled(names []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if s, ok := m.skills[name]; ok && s.Builtin {
			log.Printf("内置 skill %s 不能被禁用，已忽略", name)
			continue
		}
		next[name] = struct{}{}
	}
	m.disabled = next
}

// SetEnabled 启用或禁用指定 skill；内置 skill 不允许禁用。
func (m *SkillManager) SetEnabled(name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.skills[name]
	if !ok {
		return fmt.Errorf("skill '%s' 不存在", name)
	}
	if !enabled && s.Builtin {
		return fmt.Errorf("内置 skill '%s' 不能禁用", name)
	}
	if m.disabled == nil {
		m.disabled = make(map[string]struct{})
	}
	if enabled {
		delete(m.disabled, name)
	} else {
		m.disabled[name] = struct{}{}
	}
	return nil
}

// DisabledNames 返回当前被禁用的 skill 名称（排序，便于写入配置）。
func (m *SkillManager) DisabledNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.disabled))
	for name := range m.disabled {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ForgetDisabled 从禁用集合中移除一个名称（删除 skill 后清理残留配置）。
func (m *SkillManager) ForgetDisabled(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.disabled[name]; !ok {
		return false
	}
	delete(m.disabled, name)
	return true
}

// List 返回可用（未禁用）skill 的元数据列表（按名称排序：map 遍历顺序随机，
// 列表会作为 skill_read 的工具结果文本回填给 LLM，必须保证输出确定）。
func (m *SkillManager) List() []*SkillMeta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*SkillMeta, 0, len(m.skills))
	for name, s := range m.skills {
		if _, off := m.disabled[name]; off {
			continue
		}
		meta := s.Meta // 复制，避免外部修改
		result = append(result, &meta)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// ListAll 返回全部 skill 的元数据列表（含被禁用的），供管理面板展示。
func (m *SkillManager) ListAll() []*SkillMeta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*SkillMeta, 0, len(m.skills))
	for _, s := range m.skills {
		meta := s.Meta // 复制，避免外部修改
		result = append(result, &meta)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// BuildAvailableSkillsPrompt 生成注入 system prompt 的 <skills_registry> 块。
// 被禁用的 skill 不输出，其描述不会进入上下文。
func (m *SkillManager) BuildAvailableSkillsPrompt() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 按 skill 名排序输出：map 遍历顺序随机，若直接拼接会导致每次请求的
	// system prompt 内容不同，打失上游 prompt 前缀缓存（见 toolsWithSession）
	names := make([]string, 0, len(m.skills))
	for name := range m.skills {
		if _, off := m.disabled[name]; off {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("\n<agent_skills_registry>\n")
	sb.WriteString("  <instruction>\n")
	sb.WriteString("    The following skills define your operational logic and constraints.\n")
	sb.WriteString("    BEFORE invoking any tool, you MUST match the intent against these skills first.\n")
	sb.WriteString("  </instruction>\n\n")

	for _, name := range names {
		s := m.skills[name]
		sb.WriteString("  <skill>\n")
		sb.WriteString(fmt.Sprintf("    <name>%s</name>\n", s.Meta.Name))
		sb.WriteString(fmt.Sprintf("    <description>%s</description>\n", s.Meta.Description))
		sb.WriteString("  </skill>\n")
	}

	sb.WriteString("</agent_skills_registry>\n")
	return sb.String()
}

// RegisterToExecuter 将 skill 读取/刷新工具注册到 ToolExecuter
// 调用此方法后，Agent 就拥有了读取 SKILL.md 与刷新 skill 缓存的能力
func (m *SkillManager) RegisterToExecuter(executer *ToolExecuter) {
	executer.Register(NewSkillReadTool(m))
	executer.Register(NewSkillReloadTool(m))
}

// ValidateSkillDir 校验指定目录下的 SKILL.md（含附属文件）能否被正常加载
func ValidateSkillDir(skillDir string) error {
	_, err := loadSkillFromDir(filepath.Join(skillDir, "SKILL.md"), skillDir)
	return err
}

// loadSkillFromFile 从 SKILL.md 文件路径解析 Skill（单文件模式，无附属文件）
func loadSkillFromFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}

	content := string(data)
	meta, err := parseSkillFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("解析 frontmatter 失败: %w", err)
	}

	if meta.Name == "" {
		// 回退：使用目录名作为 skill name
		meta.Name = filepath.Base(filepath.Dir(path))
	}

	return &Skill{
		Meta:       *meta,
		Content:    content,
		Path:       path,
		DirPath:    filepath.Dir(path),
		References: make(map[string]string),
		ExtraFiles: make(map[string]string),
	}, nil
}

// loadSkillFromDir 从子目录加载 SKILL.md 和所有附属文件（递归扫描子目录）
func loadSkillFromDir(skillPath, skillDir string) (*Skill, error) {
	skill, err := loadSkillFromFile(skillPath)
	if err != nil {
		return nil, err
	}

	// 递归扫描目录下的附属文件（跳过 SKILL.md 本身）
	err = filepath.Walk(skillDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过无法访问的路径
		}

		// 跳过目录本身和 SKILL.md
		if info.IsDir() {
			return nil
		}
		if strings.EqualFold(info.Name(), "SKILL.MD") && filepath.Dir(path) == skillDir {
			return nil
		}

		// 计算相对于 skillDir 的路径作为 key
		relPath, err := filepath.Rel(skillDir, path)
		if err != nil {
			return nil
		}
		// 统一使用正斜杠
		relPath = filepath.ToSlash(relPath)

		absPath, _ := filepath.Abs(path)
		if isMarkdownFile(info.Name()) {
			data, err := os.ReadFile(absPath)
			if err != nil {
				log.Println("读取Skill附属md文件失败: ", absPath)
				return nil
			}
			skill.References[relPath] = string(data)
		} else {
			skill.ExtraFiles[relPath] = absPath
		}

		return nil
	})
	if err != nil {
		return skill, nil // 遍历失败仍返回已加载的主文件
	}

	return skill, nil
}

// isMarkdownFile 根据扩展名判断是否为 Markdown 文件
func isMarkdownFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown"
}

// SkillNameFromContent 从 SKILL.md 文本解析 frontmatter 中的技能名；
// 无 frontmatter 或名称为空时返回空串（调用方决定回退目录名）。
func SkillNameFromContent(content string) string {
	meta, err := parseSkillFrontmatter(content)
	if err != nil || meta == nil {
		return ""
	}
	return meta.Name
}

// parseSkillFrontmatter 解析 SKILL.md 中的 YAML frontmatter（--- 块）
func parseSkillFrontmatter(content string) (*SkillMeta, error) {
	// 检查是否以 --- 开头
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return &SkillMeta{}, nil
	}

	// 找到 frontmatter 结束位置
	trimmed := strings.TrimSpace(content)
	// 跳过第一个 ---
	rest := trimmed[3:]
	before, _, ok := strings.Cut(rest, "---")
	if !ok {
		return &SkillMeta{}, nil
	}

	yamlContent := before
	var meta SkillMeta
	if err := yaml.Unmarshal([]byte(yamlContent), &meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

// ─────────────────────────────────────────────
// SkillReadTool：让 Agent 调用来读取 SKILL.md
// ─────────────────────────────────────────────

// SkillReadParams 是调用 skill_read 工具时的参数
type SkillReadParams struct {
	SkillName string `json:"skill_name" desc:"要读取的 skill 名称，从 available_skills 列表中选择"`
	FileName  string `json:"file_name,omitempty" desc:"可选，skill 目录下的附属文件名（如 reference.md）。不填则返回 SKILL.md 主文件及其附属文件列表"`
}

// SkillReadTool 工具：读取指定 skill 的完整 SKILL.md 内容
type SkillReadTool struct {
	BaseTool[SkillReadParams]
	manager *SkillManager
}

// NewSkillReadTool 创建 SkillReadTool
func NewSkillReadTool(manager *SkillManager) *SkillReadTool {
	return &SkillReadTool{
		BaseTool: MakeBaseTool(
			"skill_read",
			"读取指定 skill 的详细指令内容。当你需要使用某个 skill 时，先调用此工具获取其完整指令，再按照指令执行任务。参数 skill_name 从 <available_skills> 列表中选择。如需读取附属文档（如 reference.md），传入 file_name 参数。skill 中的脚本文件（.sh/.py等）可通过 bash 工具执行（.sh 脚本用 `sh 脚本路径` 运行，不要假设 bash 存在），无需先读取。",
			SkillReadParams{},
		),
		manager: manager,
	}
}

func (t *SkillReadTool) Execute(ctx context.Context, params any, callbacks CallBackFuncs) (string, error) {
	p := params.(*SkillReadParams)

	if p.SkillName == "" {
		// 没有指定名称时，返回所有可用 skill 列表
		metas := t.manager.List()
		if len(metas) == 0 {
			return "当前没有可用的 skill。", nil
		}
		var sb strings.Builder
		sb.WriteString("可用的 skill 列表：\n")
		for _, m := range metas {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", m.Name, m.Description))
		}
		return sb.String(), nil
	}

	log.Println("[SKILL查询] 正在查询", p.SkillName)
	skill, ok := t.manager.Get(p.SkillName)
	if !ok {
		// 返回可用列表，便于 Agent 纠正
		metas := t.manager.List()
		names := make([]string, 0, len(metas))
		for _, m := range metas {
			names = append(names, m.Name)
		}
		return "", fmt.Errorf("skill '%s' 不存在，可用的 skill: [%s]",
			p.SkillName, strings.Join(names, ", "))
	}
	if t.manager.IsDisabled(p.SkillName) {
		// 已禁用：描述不进上下文，也不允许按名读取
		return "", fmt.Errorf("skill '%s' 已被禁用，不能使用", p.SkillName)
	}

	// 请求特定附属文件
	if p.FileName != "" {
		content, exists := skill.References[p.FileName]
		if !exists {
			available := make([]string, 0, len(skill.References))
			for name := range skill.References {
				available = append(available, name)
			}
			// 排序保证输出确定（列表会作为工具结果回填给 LLM）
			sort.Strings(available)
			if len(available) == 0 {
				return "", fmt.Errorf("skill '%s' 没有附属文件", p.SkillName)
			}
			return "", fmt.Errorf("skill '%s' 中不存在文件 '%s'，可用文件: [%s]",
				p.SkillName, p.FileName, strings.Join(available, ", "))
		}
		return content, nil
	}

	// 返回 SKILL.md 内容，附带附属文件/脚本列表
	result := skill.Content
	if len(skill.References) > 0 || len(skill.ExtraFiles) > 0 {
		var sb strings.Builder
		sb.WriteString(result)
		sb.WriteString("\n\n---\n\n")
		if len(skill.References) > 0 {
			sb.WriteString("## [reference文档]\n\n")
			sb.WriteString("通过 skill_read 的 file_name 参数按需读取：\n")
			// 按名排序：map 遍历顺序随机，清单作为工具结果回填必须确定
			refNames := make([]string, 0, len(skill.References))
			for name := range skill.References {
				refNames = append(refNames, name)
			}
			sort.Strings(refNames)
			for _, name := range refNames {
				sb.WriteString(fmt.Sprintf("- %s\n", name))
			}
		}
		if len(skill.ExtraFiles) > 0 {
			sb.WriteString("\n## [附带文件]\n\n")
			sb.WriteString("无需读取，可通过 bash 工具执行（.sh 脚本用 `sh 脚本路径` 运行）：\n")
			extraNames := make([]string, 0, len(skill.ExtraFiles))
			for name := range skill.ExtraFiles {
				extraNames = append(extraNames, name)
			}
			sort.Strings(extraNames)
			for _, name := range extraNames {
				sb.WriteString(fmt.Sprintf("- %s → %s\n", name, skill.ExtraFiles[name]))
			}
		}
		result = sb.String()
	}

	// 防御性截断：异常巨大的 SKILL.md/附属内容不能直接撑爆 LLM 上下文
	if r := []rune(result); len(r) > maxSkillReadRunes {
		return string(r[:maxSkillReadRunes]) + "\n...(skill 内容过长已截断，可通过 file_name 参数按需读取附属文件)", nil
	}
	return result, nil
}

// maxSkillReadRunes skill_read 单次返回的 rune 上限（宽松于工具结果截断：
// skill 是核心指令，正常内容远小于该值）。
const maxSkillReadRunes = 16000

// ─────────────────────────────────────────────
// SkillReloadTool：让 Agent 在直接编辑本地 skill 文件后刷新缓存
// ─────────────────────────────────────────────

// SkillReloadParams 是调用 skill_reload 工具时的参数（无参数）
type SkillReloadParams struct{}

// SkillReloadTool 工具：重新从磁盘加载全部 skill 并刷新缓存。
// 缓存中的 skill 内容在加载时固定，AI 通过 bash 等工具直接编辑本地
// SKILL.md/附属文档后缓存不会自动更新，需要调用本工具刷新。
type SkillReloadTool struct {
	BaseTool[SkillReloadParams]
	manager *SkillManager
}

// NewSkillReloadTool 创建 SkillReloadTool
func NewSkillReloadTool(manager *SkillManager) *SkillReloadTool {
	return &SkillReloadTool{
		BaseTool: MakeBaseTool(
			"skill_reload",
			"重新从磁盘加载全部 skill 并刷新缓存。当你通过 bash 等工具直接编辑了本地 skill 文件（SKILL.md 或附属文档）后，缓存中的内容不会自动更新，必须调用本工具刷新，后续的 skill_read 与技能匹配才会使用最新内容。未修改文件时无需调用。",
			SkillReloadParams{},
		),
		manager: manager,
	}
}

func (t *SkillReloadTool) Execute(ctx context.Context, params any, callbacks CallBackFuncs) (string, error) {
	count, err := t.manager.Refresh()
	if err != nil {
		return "", err
	}
	metas := t.manager.List()
	names := make([]string, 0, len(metas))
	for _, meta := range metas {
		names = append(names, meta.Name)
	}
	log.Printf("[SKILL刷新] 已重新加载 %d 个 skill", count)
	return fmt.Sprintf("skill 缓存已刷新，当前共加载 %d 个 skill：[%s]", count, strings.Join(names, ", ")), nil
}
