package llmtool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const testSkillContent = `---
name: test-skill
description: 测试技能
---

# 测试技能 v1
`

func writeTestSkill(t *testing.T, dir, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, "test-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("创建 skill 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("写入 SKILL.md 失败: %v", err)
	}
}

func TestSkillManagerRefresh(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, testSkillContent)

	m := NewSkillManager()
	if err := m.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir error: %v", err)
	}
	skill, ok := m.Get("test-skill")
	if !ok {
		t.Fatal("skill 未加载")
	}
	if !strings.Contains(skill.Content, "v1") {
		t.Fatalf("初始内容不符: %s", skill.Content)
	}

	// 直接修改磁盘文件（模拟 AI 用 bash 编辑本地 skill 文件）
	writeTestSkill(t, dir, strings.Replace(testSkillContent, "v1", "v2", 1))

	count, err := m.Refresh()
	if err != nil {
		t.Fatalf("Refresh error: %v", err)
	}
	if count != 1 {
		t.Fatalf("Refresh 后 skill 数量不符: %d", count)
	}
	skill, ok = m.Get("test-skill")
	if !ok {
		t.Fatal("Refresh 后 skill 丢失")
	}
	if !strings.Contains(skill.Content, "v2") {
		t.Fatalf("Refresh 后内容未更新: %s", skill.Content)
	}
}

func TestSkillManagerRefresh_NotLoaded(t *testing.T) {
	m := NewSkillManager()
	if _, err := m.Refresh(); err == nil {
		t.Fatal("未从目录加载时 Refresh 应返回错误")
	}
}

func TestSkillReloadTool(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, testSkillContent)

	m := NewSkillManager()
	if err := m.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir error: %v", err)
	}
	tool := NewSkillReloadTool(m)
	out, err := tool.Execute(context.Background(), &SkillReloadParams{}, CallBackFuncs{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "1 个 skill") || !strings.Contains(out, "test-skill") {
		t.Fatalf("刷新结果提示不符: %s", out)
	}
}

// TestBuiltinSkills 校验内置技能：物化到磁盘、不可禁用、不受白名单影响，
// 且外置技能的启用/禁用与持久化重载行为正确。
func TestBuiltinSkills(t *testing.T) {
	dir := t.TempDir()
	writeTestSkill(t, dir, testSkillContent)

	fsys := fstest.MapFS{
		"skills/web/SKILL.md":  &fstest.MapFile{Data: []byte("---\nname: web\ndescription: 内置联网技能\n---\n\n# web\n")},
		"skills/web/webctl.py": &fstest.MapFile{Data: []byte("print('hi')\n")},
	}
	m := NewSkillManager()
	m.EnableBuiltinSkills(fsys, "skills")
	// 显式白名单只列外置技能：内置技能仍应加载
	if err := m.LoadFromDirWithFilter(dir, []string{"test-skill"}); err != nil {
		t.Fatalf("LoadFromDirWithFilter error: %v", err)
	}

	web, ok := m.Get("web")
	if !ok || !web.Builtin {
		t.Fatalf("内置技能未加载或未标记 Builtin: %+v", web)
	}
	if _, ok := web.ExtraFiles["webctl.py"]; !ok {
		t.Fatalf("内置技能脚本未登记为附带文件: %+v", web.ExtraFiles)
	}
	scriptPath := filepath.Join(dir, ".builtin", "web", "webctl.py")
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("内置脚本未物化到磁盘: %v", err)
	}

	// 内置技能不可禁用
	if err := m.SetEnabled("web", false); err == nil {
		t.Fatal("禁用内置技能应报错")
	}
	// 外置技能可禁用：描述不再进入 prompt，skill_read 拒绝读取
	if err := m.SetEnabled("test-skill", false); err != nil {
		t.Fatalf("禁用外置技能失败: %v", err)
	}
	if !m.IsDisabled("test-skill") {
		t.Fatal("外置技能禁用状态未生效")
	}
	prompt := m.BuildAvailableSkillsPrompt()
	if !strings.Contains(prompt, "<name>web</name>") || strings.Contains(prompt, "test-skill") {
		t.Fatalf("禁用后 prompt 内容不符: %s", prompt)
	}
	readTool := NewSkillReadTool(m)
	if _, err := readTool.Execute(context.Background(), &SkillReadParams{SkillName: "test-skill"}, CallBackFuncs{}); err == nil {
		t.Fatal("读取已禁用技能应报错")
	}

	// 重新加载后禁用状态与内置技能都保留
	if err := m.Reload(dir, nil); err != nil {
		t.Fatalf("Reload error: %v", err)
	}
	if !m.IsDisabled("test-skill") {
		t.Fatal("Reload 后禁用状态丢失")
	}
	if _, ok := m.Get("web"); !ok {
		t.Fatal("Reload 后内置技能丢失")
	}

	// SetDisabled 忽略内置技能（即使配置里写了）
	m.SetDisabled([]string{"web", "test-skill"})
	if m.IsDisabled("web") {
		t.Fatal("内置技能不应出现在禁用集合")
	}
	if names := m.DisabledNames(); len(names) != 1 || names[0] != "test-skill" {
		t.Fatalf("禁用集合不符: %v", names)
	}
}

func TestRemoveMCP_NotRegistered(t *testing.T) {
	e := NewToolExecuter()
	if err := e.RemoveMCP("no-such-server"); err == nil {
		t.Fatal("移除未注册的 MCP 服务器应返回错误")
	}
	if err := e.ReconnectMCP(context.Background(), "no-such-server"); err == nil {
		t.Fatal("重连未注册的 MCP 服务器应返回错误")
	}
	if infos := e.MCPServerInfos(); len(infos) != 0 {
		t.Fatalf("空执行器的 MCP 服务器列表应为空: %v", infos)
	}
}

func TestAddMCP_ConnectFailure(t *testing.T) {
	e := NewToolExecuter()
	// 不存在的命令：连接必然失败，且不应留下残余注册
	err := e.AddMCP(context.Background(), &MCPConfig{Name: "bad-server", Command: "definitely-not-a-real-command-xyz"}, true)
	if err == nil {
		t.Fatal("连接失败的 AddMCP 应返回错误")
	}
	if infos := e.MCPServerInfos(); len(infos) != 0 {
		t.Fatalf("AddMCP 失败后不应有残余注册: %v", infos)
	}
}
