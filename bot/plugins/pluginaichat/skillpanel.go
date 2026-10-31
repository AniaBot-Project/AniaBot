package pluginaichat

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// skill 面板管理接口（实现 adminpanel.SkillSource）。
// 上传/删除/启停后立即从磁盘热重载或更新 skill 注册表，无需重启；
// 若配置了 skills 白名单，未列入白名单的外置技能不会加载（内置技能始终加载）。

// skillsDisabledConfigKey 保存被禁用的外置 skill 名称（面板开关持久化位置）。
const skillsDisabledConfigKey = "plugin.ai_chat_bot.skills_disabled"

// 单个 skill 解压后的体积与文件数上限
const (
	skillMaxTotalBytes = 64 << 20 // 64 MiB
	skillMaxFiles      = 500

	// skillContentMaxBytes AI 直接撰写 SKILL.md 内容的体积上限（防止撑爆磁盘/上下文）
	skillContentMaxBytes = 256 << 10 // 256 KiB

	// skillPreviewMaxBytes 面板预览单个附带文本文件的内容体积上限（防止超大文件撑爆面板）
	skillPreviewMaxBytes = 512 << 10 // 512 KiB
)

// SkillList 返回当前 skill 列表（含内置与已禁用的）、skills 目录与白名单（供 Web 面板展示）。
func (p *AIChatPlugin) SkillList() ([]plugininfo.SkillInfo, string, []string) {
	if p.skillManager == nil {
		return nil, p.skillsDir, p.cfg.Skills
	}
	metas := p.skillManager.ListAll()
	infos := make([]plugininfo.SkillInfo, 0, len(metas))
	for _, meta := range metas {
		infos = append(infos, p.buildSkillInfo(meta))
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, p.skillsDir, p.cfg.Skills
}

// buildSkillInfo 从 SkillMeta 组装面板/AI 展示用的 SkillInfo。
func (p *AIChatPlugin) buildSkillInfo(meta *llmtool.SkillMeta) plugininfo.SkillInfo {
	info := plugininfo.SkillInfo{
		Name:        meta.Name,
		Description: meta.Description,
	}
	if skill, ok := p.skillManager.Get(meta.Name); ok {
		info.Builtin = skill.Builtin
		info.Disabled = p.skillManager.IsDisabled(meta.Name)
		info.Location = skillLocation(p.skillsDir, skill.Path)
		for name := range skill.References {
			info.Refs = append(info.Refs, name)
		}
		for name := range skill.ExtraFiles {
			info.Extras = append(info.Extras, name)
		}
		sort.Strings(info.Refs)
		sort.Strings(info.Extras)
	}
	return info
}

// SkillDetail 返回 SKILL.md 完整内容与附属文件信息（供 Web 面板查看详情）。
func (p *AIChatPlugin) SkillDetail(name string) (plugininfo.SkillDetail, error) {
	if p.skillManager == nil {
		return plugininfo.SkillDetail{}, fmt.Errorf("skill 功能未初始化")
	}
	skill, ok := p.skillManager.Get(name)
	if !ok {
		return plugininfo.SkillDetail{}, fmt.Errorf("skill '%s' 不存在", name)
	}
	detail := plugininfo.SkillDetail{
		Name:        skill.Meta.Name,
		Description: skill.Meta.Description,
		Location:    skillLocation(p.skillsDir, skill.Path),
		Content:     skill.Content,
		Builtin:     skill.Builtin,
		Disabled:    p.skillManager.IsDisabled(name),
	}
	for rel, content := range skill.References {
		detail.Files = append(detail.Files, plugininfo.SkillFileInfo{
			Name:    rel,
			Kind:    "reference",
			Size:    int64(len(content)),
			Content: content,
		})
	}
	for rel, absPath := range skill.ExtraFiles {
		info := plugininfo.SkillFileInfo{
			Name: rel,
			Kind: "extra",
		}
		if stat, err := os.Stat(absPath); err == nil {
			info.Size = stat.Size()
			if content, ok := readTextPreview(absPath, stat.Size()); ok {
				info.Content = content
			}
		}
		detail.Files = append(detail.Files, info)
	}
	sort.Slice(detail.Files, func(i, j int) bool { return detail.Files[i].Name < detail.Files[j].Name })
	return detail, nil
}

// readTextPreview 读取附带文本文件供面板预览：体积超过上限或内容不是
// 合法 UTF-8 文本（含 NUL 字节，如二进制文件）时不返回内容，面板仅展示文件信息。
func readTextPreview(absPath string, size int64) (string, bool) {
	if size <= 0 || size > skillPreviewMaxBytes {
		return "", false
	}
	f, err := os.Open(absPath)
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, skillPreviewMaxBytes+1))
	if err != nil || int64(len(data)) > skillPreviewMaxBytes {
		return "", false
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", false
	}
	return string(data), true
}

// SkillDelete 按名称删除 skill：从磁盘移除对应目录/文件后热重载注册表。
// 内置 skill 不允许删除。
func (p *AIChatPlugin) SkillDelete(name string) error {
	if p.skillManager == nil {
		return fmt.Errorf("skill 功能未初始化")
	}
	skill, ok := p.skillManager.Get(name)
	if !ok {
		return fmt.Errorf("skill '%s' 不存在", name)
	}
	if skill.Builtin {
		return fmt.Errorf("内置 skill '%s' 不能删除", name)
	}

	skillDirAbs, err1 := filepath.Abs(filepath.Dir(skill.Path))
	skillsDirAbs, err2 := filepath.Abs(p.skillsDir)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("解析路径失败")
	}

	var err error
	if skillDirAbs == skillsDirAbs {
		// 单文件模式：skillsDir/SKILL.md，只删除文件本身
		err = os.Remove(skill.Path)
	} else {
		// 目录模式：删除整个 skill 目录
		err = os.RemoveAll(filepath.Dir(skill.Path))
	}
	if err != nil {
		return fmt.Errorf("删除失败: %w", err)
	}

	if err := p.skillManager.Reload(p.skillsDir, p.cfg.Skills); err != nil {
		return fmt.Errorf("已删除，但重载 skill 失败: %w", err)
	}
	if p.skillManager.ForgetDisabled(name) {
		p.persistDisabledSkills()
	}
	p.Logger.Info("skill 已删除", "skill", name)
	return nil
}

// SkillSetEnabled 启用/禁用外置 skill：立即更新注册表（禁用后描述不再进入
// available_skills，skill_read 也会拒绝），并把禁用列表写入配置中心持久化。
// 内置 skill 不可禁用；配置中心不可用时仅本次运行内生效。
func (p *AIChatPlugin) SkillSetEnabled(name string, enabled bool) error {
	if p.skillManager == nil {
		return fmt.Errorf("skill 功能未初始化")
	}
	if err := p.skillManager.SetEnabled(name, enabled); err != nil {
		return err
	}
	p.cfg.SkillsDisabled = p.skillManager.DisabledNames()
	action := "启用"
	if !enabled {
		action = "禁用"
	}
	if p.ConfigEditor == nil {
		p.Logger.Warn("配置中心不可用，skill 启停状态仅在本次运行内生效", "skill", name, "action", action)
		return nil
	}
	if err := p.ConfigEditor.Set(skillsDisabledConfigKey, p.cfg.SkillsDisabled); err != nil {
		return fmt.Errorf("已%s skill %s，但写入配置失败（重启后会恢复）：%w", action, name, err)
	}
	p.Logger.Info("skill 启停状态已更新", "skill", name, "action", action)
	return nil
}

// persistDisabledSkills 把当前禁用列表写回配置中心（删除 skill 后清理残留名称）。
func (p *AIChatPlugin) persistDisabledSkills() {
	p.cfg.SkillsDisabled = p.skillManager.DisabledNames()
	if p.ConfigEditor == nil {
		return
	}
	if err := p.ConfigEditor.Set(skillsDisabledConfigKey, p.cfg.SkillsDisabled); err != nil {
		p.Logger.Warn("写入 skill 禁用状态失败", "error", err.Error())
	}
}

// SkillUpload 从 zip 压缩包内容安装 skill 并热重载（面板入口，行为不变）。
// zip 结构支持两种形式：
//   - 根目录直接包含 SKILL.md（以压缩包文件名作为 skill 目录名）
//   - 单一顶层目录，其中包含 SKILL.md（以顶层目录名作为 skill 目录名）
func (p *AIChatPlugin) SkillUpload(filename string, data []byte) error {
	_, err := p.installSkillZip(filename, data, "")
	return err
}

// installSkillZip 从 zip 数据安装 skill 并热重载，返回安装后的 skill 元信息。
// dirName 非空时覆盖目标目录名（zip 的根 SKILL.md / 顶层目录两种形式均可）。
// 与面板 SkillUpload 共用同一套 zip-slip / 体积 / 校验防护。
func (p *AIChatPlugin) installSkillZip(filename string, data []byte, dirName string) (plugininfo.SkillInfo, error) {
	if p.skillManager == nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("skill 功能未初始化")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("无法读取压缩包（请上传 zip 格式）: %w", err)
	}
	if len(zr.File) > skillMaxFiles {
		return plugininfo.SkillInfo{}, fmt.Errorf("压缩包内文件过多（上限 %d 个）", skillMaxFiles)
	}

	// 检测目录结构：剥离的顶层前缀与目标目录名
	prefix, detectedDir, err := detectSkillRoot(zr, filename)
	if err != nil {
		return plugininfo.SkillInfo{}, err
	}
	if dirName != "" {
		if sanitizeSkillDirName(dirName) == "" {
			return plugininfo.SkillInfo{}, fmt.Errorf("目标目录名不合法: %s", dirName)
		}
		detectedDir = dirName
	}

	if err := os.MkdirAll(p.skillsDir, 0o755); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("创建 skills 目录失败: %w", err)
	}

	// 先解压到临时目录，校验通过后再替换目标目录
	tmpDir, err := os.MkdirTemp(p.skillsDir, ".upload-*")
	if err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractSkillZip(zr, prefix, tmpDir); err != nil {
		return plugininfo.SkillInfo{}, err
	}

	if err := llmtool.ValidateSkillDir(tmpDir); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("SKILL.md 校验失败: %w", err)
	}

	target := filepath.Join(p.skillsDir, detectedDir)
	if err := os.RemoveAll(target); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("清理旧版本失败: %w", err)
	}
	if err := os.Rename(tmpDir, target); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("安装失败: %w", err)
	}

	if err := p.skillManager.Reload(p.skillsDir, p.cfg.Skills); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("已安装，但重载 skill 失败: %w", err)
	}
	p.Logger.Info("skill 已上传", "dir", detectedDir, "file", filename)

	return p.skillInfoByDir(detectedDir)
}

// SkillInstallFromContent 将一段 SKILL.md 全文安装为 skill（目录 <name>/SKILL.md），
// 校验通过后热重载。name 为空时依次回退 frontmatter name / "skill"；
// 返回安装后的 skill 元信息（frontmatter name 可能与目录名不同）。
func (p *AIChatPlugin) SkillInstallFromContent(name, content string) (plugininfo.SkillInfo, error) {
	if p.skillManager == nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("skill 功能未初始化")
	}
	if strings.TrimSpace(content) == "" {
		return plugininfo.SkillInfo{}, fmt.Errorf("SKILL.md 内容不能为空")
	}
	if len(content) > skillContentMaxBytes {
		return plugininfo.SkillInfo{}, fmt.Errorf("SKILL.md 内容过大（上限 %d KB）", skillContentMaxBytes>>10)
	}
	name = sanitizeSkillDirName(name)
	if name == "" {
		name = sanitizeSkillDirName(llmtool.SkillNameFromContent(content))
	}
	if name == "" {
		name = "skill"
	}

	if err := os.MkdirAll(p.skillsDir, 0o755); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("创建 skills 目录失败: %w", err)
	}
	tmpDir, err := os.MkdirTemp(p.skillsDir, ".upload-*")
	if err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("写入 SKILL.md 失败: %w", err)
	}
	if err := llmtool.ValidateSkillDir(tmpDir); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("SKILL.md 校验失败: %w", err)
	}

	target := filepath.Join(p.skillsDir, name)
	if err := os.RemoveAll(target); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("清理旧版本失败: %w", err)
	}
	if err := os.Rename(tmpDir, target); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("安装失败: %w", err)
	}

	if err := p.skillManager.Reload(p.skillsDir, p.cfg.Skills); err != nil {
		return plugininfo.SkillInfo{}, fmt.Errorf("已安装，但重载 skill 失败: %w", err)
	}
	p.Logger.Info("skill 已创建", "dir", name)

	return p.skillInfoByDir(name)
}

// skillInfoByDir 按安装目录（如 "my-skill"）查找注册表中该目录下的 skill 元信息。
// frontmatter name 可能与目录名不同，因此按路径匹配而非名称。
func (p *AIChatPlugin) skillInfoByDir(dirName string) (plugininfo.SkillInfo, error) {
	prefix := filepath.ToSlash(dirName) + "/"
	for _, meta := range p.skillManager.ListAll() {
		skill, ok := p.skillManager.Get(meta.Name)
		if !ok {
			continue
		}
		loc := filepath.ToSlash(skillLocation(p.skillsDir, skill.Path))
		if loc == filepath.ToSlash(dirName)+"/SKILL.md" || strings.HasPrefix(loc, prefix) {
			return p.buildSkillInfo(meta), nil
		}
	}
	// 目录已落盘但注册表里没有对应条目：可能是白名单排除，或名称与内置技能冲突
	if data, err := os.ReadFile(filepath.Join(p.skillsDir, dirName, "SKILL.md")); err == nil {
		name := llmtool.SkillNameFromContent(string(data))
		if s, ok := p.skillManager.Get(name); ok && s.Builtin {
			return plugininfo.SkillInfo{}, fmt.Errorf("技能名 %s 与内置技能冲突，未被加载，请修改 frontmatter 的 name 后重新安装", name)
		}
	}
	return plugininfo.SkillInfo{}, fmt.Errorf("skill 已安装但未加载（可能不在 skills 白名单中）")
}

// detectSkillRoot 判断 zip 内 skill 的顶层前缀，返回 (前缀, skill 目录名)。
func detectSkillRoot(zr *zip.Reader, filename string) (string, string, error) {
	hasRootSkill := false
	topDirs := make(map[string]struct{})
	topDirHasSkill := make(map[string]bool)

	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if f.FileInfo().IsDir() || name == "" {
			continue
		}
		parts := strings.Split(name, "/")
		if slices.Contains(parts, "..") {
			return "", "", fmt.Errorf("压缩包包含非法路径: %s", f.Name)
		}
		if len(parts) == 1 {
			if strings.EqualFold(parts[0], "SKILL.md") {
				hasRootSkill = true
			}
		} else {
			top := parts[0]
			topDirs[top] = struct{}{}
			if len(parts) == 2 && strings.EqualFold(parts[1], "SKILL.md") {
				topDirHasSkill[top] = true
			}
		}
	}

	// 形式一：根目录直接包含 SKILL.md
	if hasRootSkill {
		dirName := sanitizeSkillDirName(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
		if dirName == "" {
			dirName = "skill"
		}
		return "", dirName, nil
	}

	// 形式二：单一顶层目录包含 SKILL.md
	if len(topDirs) == 1 {
		for top := range topDirs {
			if topDirHasSkill[top] {
				dirName := sanitizeSkillDirName(top)
				if dirName == "" {
					return "", "", fmt.Errorf("顶层目录名不合法: %s", top)
				}
				return top + "/", dirName, nil
			}
		}
	}

	return "", "", fmt.Errorf("压缩包中未找到 SKILL.md（应位于根目录或单一顶层目录下）")
}

// sanitizeSkillDirName 校验/清洗 skill 目录名（禁止路径分隔符、.. 与隐藏目录）。
func sanitizeSkillDirName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	if strings.ContainsAny(name, `/\`) {
		return ""
	}
	// 以 . 开头的是保留目录（如 .builtin 内置技能物化目录、上传临时目录）
	if strings.HasPrefix(name, ".") {
		return ""
	}
	return name
}

// extractSkillZip 将 zip 中 prefix 前缀下的文件解压到 dstDir，带 zip-slip 与体积防护。
func extractSkillZip(zr *zip.Reader, prefix, dstDir string) error {
	var total int64
	count := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" {
			continue
		}
		// 防御：拒绝绝对路径与 ..
		clean := path.Clean(rel)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("压缩包包含非法路径: %s", f.Name)
		}

		total += int64(f.UncompressedSize64)
		if total > skillMaxTotalBytes {
			return fmt.Errorf("解压后体积超过上限（%d MB）", skillMaxTotalBytes>>20)
		}
		count++

		target := filepath.Join(dstDir, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("创建目录失败: %w", err)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("读取压缩包文件失败: %w", err)
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode())
		if err != nil {
			rc.Close()
			return fmt.Errorf("写入文件失败: %w", err)
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return fmt.Errorf("解压文件失败: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("写入文件失败: %w", closeErr)
		}
	}
	if count == 0 {
		return fmt.Errorf("压缩包内容为空")
	}
	return nil
}

// skillLocation 返回 skill 主文件相对于 skillsDir 的展示路径。
func skillLocation(skillsDir, skillPath string) string {
	rel, err := filepath.Rel(skillsDir, skillPath)
	if err != nil {
		return skillPath
	}
	return filepath.ToSlash(rel)
}
