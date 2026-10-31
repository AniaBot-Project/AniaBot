package functool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
