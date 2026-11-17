package functool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// TestCreateToolsWithSkillLoadsBuiltinWeb 校验内置 web 技能随工具执行器加载：
// 即使 skillsDir 尚不存在也会创建目录并把技能（含 webctl.py）物化到磁盘。
func TestCreateToolsWithSkillLoadsBuiltinWeb(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	_, manager, err := CreateToolsWithSkill(nil, dir, BashConfig{}, FileConfig{}, LocalImageConfig{}, FileToolsConfig{}, nil, true)
	if err != nil {
		t.Fatalf("CreateToolsWithSkill error: %v", err)
	}

	web, ok := manager.Get("web")
	if !ok || !web.Builtin {
		t.Fatalf("内置 web 技能未加载: %+v", web)
	}
	if _, ok := web.ExtraFiles["webctl.py"]; !ok {
		t.Fatalf("webctl.py 未登记为附带文件: %+v", web.ExtraFiles)
	}
	script := filepath.Join(dir, ".builtin", "web", "webctl.py")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("webctl.py 未物化到磁盘: %v", err)
	}
	if !strings.Contains(manager.BuildAvailableSkillsPrompt(), "<name>web</name>") {
		t.Fatal("web 技能未进入 available_skills 注册表")
	}
	// 外置技能目录不存在不应报错
	if _, err := os.Stat(filepath.Join(dir, ".builtin", "web", "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md 未物化: %v", err)
	}
}

// TestCreateToolsWithSkillLoadsBuiltinAniabot 校验「了解自身」的内置 aniabot 技能：
// 随二进制加载、描述进入技能注册表、三份参考文档可通过 skill_read 读取，
// 且作为内置技能不可禁用（内容维护在 builtinskills/skills/aniabot）。
func TestCreateToolsWithSkillLoadsBuiltinAniabot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	_, manager, err := CreateToolsWithSkill(nil, dir, BashConfig{}, FileConfig{}, LocalImageConfig{}, FileToolsConfig{}, nil, true)
	if err != nil {
		t.Fatalf("CreateToolsWithSkill error: %v", err)
	}

	skill, ok := manager.Get("aniabot")
	if !ok || !skill.Builtin {
		t.Fatalf("内置 aniabot 技能未加载: %+v", skill)
	}
	if !strings.Contains(skill.Meta.Description, "配置") {
		t.Fatalf("技能描述应说明配置指导用途: %q", skill.Meta.Description)
	}
	if !strings.Contains(manager.BuildAvailableSkillsPrompt(), "<name>aniabot</name>") {
		t.Fatal("aniabot 技能未进入 available_skills 注册表")
	}
	// 参考文档登记为附属 md（skill_read 的 file_name 取值）
	for _, ref := range []string{"reference/config.md", "reference/panel.md", "reference/troubleshooting.md"} {
		if _, ok := skill.References[ref]; !ok {
			t.Fatalf("参考文档 %s 未登记: %v", ref, skill.References)
		}
	}

	readTool := llmtool.NewSkillReadTool(manager)
	out, err := readTool.Execute(context.Background(), &llmtool.SkillReadParams{SkillName: "aniabot"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("skill_read error: %v", err)
	}
	// 主体必须给出「配置在哪、怎么改、要不要重启」这三类关键结论
	for _, want := range []string{"config_get", "重启", "reference/config.md"} {
		if !strings.Contains(out, want) {
			t.Fatalf("SKILL.md 缺少关键内容 %q", want)
		}
	}
	// 按 file_name 读参考文档
	out, err = readTool.Execute(context.Background(), &llmtool.SkillReadParams{SkillName: "aniabot", FileName: "reference/config.md"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("读取参考文档失败: %v", err)
	}
	for _, want := range []string{"bot.admin_ids", "plugin.ai_chat_bot.api_key", "files.mcp_json"} {
		if !strings.Contains(out, want) {
			t.Fatalf("配置参考文档缺少 %q", want)
		}
	}

	// 内置技能不可禁用
	if err := manager.SetEnabled("aniabot", false); err == nil {
		t.Fatal("内置 aniabot 技能不应允许禁用")
	}
}
