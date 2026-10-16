package llmtool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
