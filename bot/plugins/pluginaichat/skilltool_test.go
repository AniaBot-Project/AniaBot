package pluginaichat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

// skillTools 按注册顺序返回 skill 管理工具（list / install / remove）。
func skillTools(p *AIChatPlugin) (llmtool.Tool, llmtool.Tool, llmtool.Tool) {
	tools := newSkillTools(p)
	if len(tools) != 3 {
		panic("skill 工具数量不符")
	}
	return tools[0], tools[1], tools[2]
}

func newSkillToolPlugin(t *testing.T) *AIChatPlugin {
	t.Helper()
	p := newTestSkillPlugin(t)
	p.cfg.Skills = nil
	return p
}

func TestSkillListTool_Empty(t *testing.T) {
	p := newSkillToolPlugin(t)
	listTool, _, _ := skillTools(p)
	out, err := listTool.Execute(context.Background(), &skillListParams{}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "没有已启用的 skill") {
		t.Fatalf("空列表提示不符: %s", out)
	}
}

func TestSkillListTool_WithSkills(t *testing.T) {
	p := newSkillToolPlugin(t)
	listTool, installTool, _ := skillTools(p)

	if _, err := installTool.Execute(context.Background(), &skillInstallParams{
		Content: testSkillMD,
		Name:    "my-skill",
	}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("安装失败: %v", err)
	}

	out, err := listTool.Execute(context.Background(), &skillListParams{}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "test-skill") || !strings.Contains(out, "共 1 个") {
		t.Fatalf("列表内容不符: %s", out)
	}
}

func TestSkillInstallTool_Content(t *testing.T) {
	p := newSkillToolPlugin(t)
	_, installTool, _ := skillTools(p)

	out, err := installTool.Execute(context.Background(), &skillInstallParams{
		Content: testSkillMD,
		Name:    "writer",
	}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "test-skill") {
		t.Fatalf("安装结果未包含技能名: %s", out)
	}
	// 落盘：writer/SKILL.md
	if _, err := os.Stat(filepath.Join(p.skillsDir, "writer", "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md 未落盘: %v", err)
	}
	if _, ok := p.skillManager.Get("test-skill"); !ok {
		t.Fatal("skill 未加载到注册表")
	}
}

func TestSkillInstallTool_ContentNameFallback(t *testing.T) {
	p := newSkillToolPlugin(t)
	_, installTool, _ := skillTools(p)

	// 不传 name，回退 frontmatter name
	if _, err := installTool.Execute(context.Background(), &skillInstallParams{
		Content: testSkillMD,
	}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.skillsDir, "test-skill", "SKILL.md")); err != nil {
		t.Fatalf("frontmatter name 未作为目录名落盘: %v", err)
	}
}

func TestSkillInstallTool_Invalid(t *testing.T) {
	p := newSkillToolPlugin(t)
	_, installTool, _ := skillTools(p)

	// 无 url 无 content
	if _, err := installTool.Execute(context.Background(), &skillInstallParams{}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("无参数应报错")
	}
	// url 与 content 同时提供
	if _, err := installTool.Execute(context.Background(), &skillInstallParams{
		URL:     "https://example.com/a.zip",
		Content: testSkillMD,
	}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("url 与 content 同时提供应报错")
	}
	// 非 http/https scheme
	if _, err := installTool.Execute(context.Background(), &skillInstallParams{
		URL: "ftp://example.com/a.zip",
	}, llmtool.CallBackFuncs{}); err == nil || !strings.Contains(err.Error(), "http/https") {
		t.Fatalf("非 http/https 应报错: %v", err)
	}
}

func TestSkillInstallTool_URLZip(t *testing.T) {
	p := newSkillToolPlugin(t)
	_, installTool, _ := skillTools(p)

	data := makeZip(t, map[string]string{
		"remote-skill/SKILL.md":     testSkillMD,
		"remote-skill/reference.md": "ref",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	out, err := installTool.Execute(context.Background(), &skillInstallParams{
		URL: srv.URL + "/remote-skill.zip",
	}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "test-skill") {
		t.Fatalf("安装结果未包含技能名: %s", out)
	}
	if _, ok := p.skillManager.Get("test-skill"); !ok {
		t.Fatal("skill 未加载到注册表")
	}
	if _, err := os.Stat(filepath.Join(p.skillsDir, "remote-skill", "SKILL.md")); err != nil {
		t.Fatalf("zip 顶层目录未落盘: %v", err)
	}
}

func TestSkillInstallTool_URLRawMarkdown(t *testing.T) {
	p := newSkillToolPlugin(t)
	_, installTool, _ := skillTools(p)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(testSkillMD))
	}))
	defer srv.Close()

	out, err := installTool.Execute(context.Background(), &skillInstallParams{
		URL:  srv.URL + "/SKILL.md",
		Name: "writer",
	}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "test-skill") {
		t.Fatalf("安装结果未包含技能名: %s", out)
	}
	if _, err := os.Stat(filepath.Join(p.skillsDir, "writer", "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md 未落盘: %v", err)
	}
}

func TestSkillInstallTool_URLHTTPError(t *testing.T) {
	p := newSkillToolPlugin(t)
	_, installTool, _ := skillTools(p)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := installTool.Execute(context.Background(), &skillInstallParams{
		URL: srv.URL + "/missing.zip",
	}, llmtool.CallBackFuncs{}); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("HTTP 404 应报错: %v", err)
	}
}

func TestSkillRemoveTool(t *testing.T) {
	p := newSkillToolPlugin(t)
	_, installTool, removeTool := skillTools(p)

	if _, err := installTool.Execute(context.Background(), &skillInstallParams{
		Content: testSkillMD,
		Name:    "writer",
	}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("安装失败: %v", err)
	}

	out, err := removeTool.Execute(context.Background(), &skillRemoveParams{Name: "test-skill"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "已卸载") {
		t.Fatalf("卸载结果不符: %s", out)
	}
	if _, ok := p.skillManager.Get("test-skill"); ok {
		t.Fatal("卸载后 skill 仍在注册表")
	}
	if _, err := os.Stat(filepath.Join(p.skillsDir, "writer")); !os.IsNotExist(err) {
		t.Fatalf("卸载后目录仍存在: %v", err)
	}

	// 空 name 报错
	if _, err := removeTool.Execute(context.Background(), &skillRemoveParams{}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("空 name 应报错")
	}
}

func TestGitHubRepoPath(t *testing.T) {
	cases := []struct {
		raw string
		ok  bool
		seg []string
	}{
		{"https://github.com/owner/repo", true, []string{"owner", "repo"}},
		{"https://github.com/owner/repo/", true, []string{"owner", "repo"}},
		{"https://www.github.com/owner/repo", true, []string{"owner", "repo"}},
		{"https://github.com/owner/repo/archive/refs/heads/main.zip", false, nil},
		{"https://example.com/owner/repo", false, nil},
		{"https://raw.githubusercontent.com/owner/repo/main/SKILL.md", false, nil},
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", c.raw, err)
		}
		parts := githubRepoPath(u)
		if c.ok {
			if len(parts) != 2 || parts[0] != c.seg[0] || parts[1] != c.seg[1] {
				t.Fatalf("githubRepoPath(%s) = %v, 期望 %v", c.raw, parts, c.seg)
			}
		} else if parts != nil {
			t.Fatalf("githubRepoPath(%s) 应返回 nil, 实际 %v", c.raw, parts)
		}
	}
}
