package pluginaichat

import (
	"archive/zip"
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// newTestSkillPlugin 构造一个挂载临时 skills 目录的插件实例
func newTestSkillPlugin(t *testing.T) *AIChatPlugin {
	t.Helper()
	dir := t.TempDir()
	p := &AIChatPlugin{
		skillManager: llmtool.NewSkillManager(),
		skillsDir:    dir,
	}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return p
}

// makeZip 构造内存 zip：entries 为 路径 -> 内容
func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("创建 zip 条目失败: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("写入 zip 条目失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

const testSkillMD = "---\nname: test-skill\ndescription: 测试\n---\n# test\n"

func TestDetectSkillRoot_TopDir(t *testing.T) {
	data := makeZip(t, map[string]string{
		"my-skill/SKILL.md":   testSkillMD,
		"my-skill/ref/doc.md": "doc",
	})
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	prefix, dir, err := detectSkillRoot(zr, "whatever.zip")
	if err != nil {
		t.Fatalf("detectSkillRoot 失败: %v", err)
	}
	if prefix != "my-skill/" || dir != "my-skill" {
		t.Fatalf("前缀/目录名不符: prefix=%q dir=%q", prefix, dir)
	}
}

func TestDetectSkillRoot_RootFile(t *testing.T) {
	data := makeZip(t, map[string]string{"SKILL.md": testSkillMD})
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	prefix, dir, err := detectSkillRoot(zr, "demo.zip")
	if err != nil {
		t.Fatalf("detectSkillRoot 失败: %v", err)
	}
	if prefix != "" || dir != "demo" {
		t.Fatalf("前缀/目录名不符: prefix=%q dir=%q", prefix, dir)
	}
}

func TestDetectSkillRoot_NoSkill(t *testing.T) {
	data := makeZip(t, map[string]string{"a/readme.txt": "x", "b/SKILL.md": testSkillMD})
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := detectSkillRoot(zr, "x.zip"); err == nil {
		t.Fatal("多顶层目录时应报错")
	}
}

func TestDetectSkillRoot_RejectTraversal(t *testing.T) {
	data := makeZip(t, map[string]string{"../evil/SKILL.md": testSkillMD})
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := detectSkillRoot(zr, "x.zip"); err == nil {
		t.Fatal("包含 .. 路径时应报错")
	}
}

func TestSkillUploadAndDelete(t *testing.T) {
	dir := t.TempDir()
	p := &AIChatPlugin{
		skillManager: llmtool.NewSkillManager(),
		skillsDir:    dir,
	}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	p.cfg.Skills = nil

	// 上传（顶层目录形式）
	data := makeZip(t, map[string]string{
		"my-skill/SKILL.md":       testSkillMD,
		"my-skill/reference.md":   "ref",
		"my-skill/scripts/run.sh": "echo hi",
	})
	if err := p.SkillUpload("my-skill.zip", data); err != nil {
		t.Fatalf("SkillUpload 失败: %v", err)
	}

	skills, _, _ := p.SkillList()
	if len(skills) != 1 || skills[0].Name != "test-skill" {
		t.Fatalf("上传后 skill 列表不符: %+v", skills)
	}
	if len(skills[0].Refs) != 1 || skills[0].Refs[0] != "reference.md" {
		t.Fatalf("附属文档不符: %+v", skills[0].Refs)
	}
	if len(skills[0].Extras) != 1 || !strings.HasSuffix(skills[0].Extras[0], "run.sh") {
		t.Fatalf("附带文件不符: %+v", skills[0].Extras)
	}
	if _, err := os.Stat(filepath.Join(dir, "my-skill", "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md 未落盘: %v", err)
	}

	// 覆盖上传（根目录形式，目录名来自压缩包名 → 落为新目录）
	data2 := makeZip(t, map[string]string{"SKILL.md": "---\nname: other-skill\ndescription: 另一个\n---\n"})
	if err := p.SkillUpload("other.zip", data2); err != nil {
		t.Fatalf("覆盖上传失败: %v", err)
	}
	skills, _, _ = p.SkillList()
	if len(skills) != 2 {
		t.Fatalf("第二次上传后 skill 数量不符: %+v", skills)
	}

	// 删除
	if err := p.SkillDelete("test-skill"); err != nil {
		t.Fatalf("SkillDelete 失败: %v", err)
	}
	skills, _, _ = p.SkillList()
	if len(skills) != 1 {
		t.Fatalf("删除后应剩 1 个 skill: %+v", skills)
	}
	if _, err := os.Stat(filepath.Join(dir, "my-skill")); !os.IsNotExist(err) {
		t.Fatalf("skill 目录未被删除: %v", err)
	}

	// 删除不存在的 skill
	if err := p.SkillDelete("no-such"); err == nil {
		t.Fatal("删除不存在的 skill 应报错")
	}
}

func TestSkillDetail(t *testing.T) {
	p := newTestSkillPlugin(t)
	data := makeZip(t, map[string]string{
		"my-skill/SKILL.md":        testSkillMD,
		"my-skill/reference.md":    "ref-content",
		"my-skill/scripts/run.sh":  "echo hi",
		"my-skill/config.json":     "{\"enabled\": true}",
		"my-skill/assets/logo.png": "PNG\x00\x00binary",
	})
	if err := p.SkillUpload("my-skill.zip", data); err != nil {
		t.Fatalf("SkillUpload 失败: %v", err)
	}

	detail, err := p.SkillDetail("test-skill")
	if err != nil {
		t.Fatalf("SkillDetail 失败: %v", err)
	}
	if detail.Name != "test-skill" || detail.Description != "测试" {
		t.Fatalf("详情元信息不符: %+v", detail)
	}
	if detail.Content != testSkillMD {
		t.Fatalf("SKILL.md 内容不符: %+v", detail.Content)
	}
	if len(detail.Files) != 4 {
		t.Fatalf("附属文件数量不符: %+v", detail.Files)
	}
	byName := make(map[string]plugininfo.SkillFileInfo, len(detail.Files))
	for _, f := range detail.Files {
		byName[f.Name] = f
	}
	ref := byName["reference.md"]
	if ref.Kind != "reference" || ref.Content != "ref-content" {
		t.Fatalf("附属文档详情不符: %+v", detail.Files)
	}
	// 二进制附带文件只展示文件信息，不返回正文
	png := byName["assets/logo.png"]
	if png.Kind != "extra" || png.Size == 0 || png.Content != "" {
		t.Fatalf("二进制附带文件详情不符: %+v", detail.Files)
	}
	// 文本附带文件（sh/json 等）应可预览源码
	if sh := byName["scripts/run.sh"]; sh.Content != "echo hi" {
		t.Fatalf("sh 附带文件正文不符: %+v", sh)
	}
	if js := byName["config.json"]; js.Content != "{\"enabled\": true}" {
		t.Fatalf("json 附带文件正文不符: %+v", js)
	}

	if _, err := p.SkillDetail("no-such"); err == nil {
		t.Fatal("查看不存在的 skill 应报错")
	}
}

func TestSkillUpload_InvalidZip(t *testing.T) {
	p := newTestSkillPlugin(t)
	if err := p.SkillUpload("bad.zip", []byte("not a zip")); err == nil {
		t.Fatal("非法 zip 应报错")
	}
	// 没有 SKILL.md 的 zip
	data := makeZip(t, map[string]string{"readme.md": "hi"})
	if err := p.SkillUpload("noskill.zip", data); err == nil {
		t.Fatal("缺少 SKILL.md 应报错")
	}
}

func TestSkillSetEnabledPersists(t *testing.T) {
	p := newTestSkillPlugin(t)
	editor := newStubConfigEditor()
	p.ConfigEditor = editor

	if err := p.SkillUpload("my-skill.zip", makeZip(t, map[string]string{"my-skill/SKILL.md": testSkillMD})); err != nil {
		t.Fatalf("SkillUpload 失败: %v", err)
	}

	if err := p.SkillSetEnabled("test-skill", false); err != nil {
		t.Fatalf("禁用失败: %v", err)
	}
	if !p.skillManager.IsDisabled("test-skill") {
		t.Fatal("禁用未生效")
	}
	got, ok := editor.Get(skillsDisabledConfigKey)
	if !ok {
		t.Fatal("禁用状态未写入配置中心")
	}
	names, ok := got.([]string)
	if !ok || len(names) != 1 || names[0] != "test-skill" {
		t.Fatalf("写入的禁用列表不符: %#v", got)
	}
	if len(p.cfg.SkillsDisabled) != 1 || p.cfg.SkillsDisabled[0] != "test-skill" {
		t.Fatalf("内存配置未同步: %#v", p.cfg.SkillsDisabled)
	}

	if err := p.SkillSetEnabled("test-skill", true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	if p.skillManager.IsDisabled("test-skill") {
		t.Fatal("启用未生效")
	}
	raw, _ := editor.Get(skillsDisabledConfigKey)
	after, afterOK := raw.([]string)
	if !afterOK || len(after) != 0 {
		t.Fatalf("启用后禁用列表应为空: %#v", raw)
	}
}

func TestSkillSetEnabled_RejectsBuiltin(t *testing.T) {
	p := newTestSkillPlugin(t)
	fsys := fstest.MapFS{
		"skills/web/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: web\ndescription: 内置\n---\n")},
	}
	p.skillManager.EnableBuiltinSkills(fsys, "skills")
	if err := p.skillManager.LoadFromDir(p.skillsDir); err != nil {
		t.Fatalf("加载内置技能失败: %v", err)
	}
	if err := p.SkillSetEnabled("web", false); err == nil {
		t.Fatal("内置技能禁用应报错")
	}
	if p.skillManager.IsDisabled("web") {
		t.Fatal("内置技能不应被禁用")
	}
}

func TestSkillDelete_RejectsBuiltin(t *testing.T) {
	p := newTestSkillPlugin(t)
	fsys := fstest.MapFS{
		"skills/web/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: web\ndescription: 内置\n---\n")},
	}
	p.skillManager.EnableBuiltinSkills(fsys, "skills")
	if err := p.skillManager.LoadFromDir(p.skillsDir); err != nil {
		t.Fatalf("加载内置技能失败: %v", err)
	}
	if err := p.SkillDelete("web"); err == nil {
		t.Fatal("内置技能删除应报错")
	}
	if _, err := os.Stat(filepath.Join(p.skillsDir, ".builtin", "web", "SKILL.md")); err != nil {
		t.Fatalf("内置技能文件不应被删除: %v", err)
	}
}
