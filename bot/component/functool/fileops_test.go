package functool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

func newTestFileTools(t *testing.T, root string) (read *ReadFileTool, write *WriteFileTool, edit *EditFileTool, glob *GlobTool, grep *GrepTool) {
	t.Helper()
	cfg := FileToolsConfig{Enable: true, Root: root}
	return NewReadFileTool(cfg), NewWriteFileTool(cfg), NewEditFileTool(cfg), NewGlobTool(cfg), NewGrepTool(cfg)
}

func TestReadFileNumberedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	read, _, _, _, _ := newTestFileTools(t, dir)

	got, err := read.Execute(context.Background(), &ReadFileParams{Path: path}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("read_file 失败: %v", err)
	}
	want := "     1\tline1\n     2\tline2\n     3\tline3\n"
	if got != want {
		t.Fatalf("带行号输出不符:\ngot:\n%q\nwant:\n%q", got, want)
	}

	// offset/limit 分段
	got, err = read.Execute(context.Background(), &ReadFileParams{Path: path, Offset: 2, Limit: 1}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("read_file offset 失败: %v", err)
	}
	if !strings.Contains(got, "2\tline2") || strings.Contains(got, "line3") {
		t.Fatalf("offset/limit 未生效: %q", got)
	}
	if !strings.Contains(got, "offset=3") {
		t.Fatalf("续读提示缺失: %q", got)
	}

	// 超出范围
	got, err = read.Execute(context.Background(), &ReadFileParams{Path: path, Offset: 99}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(got, "超出文件范围") {
		t.Fatalf("超出范围应提示: %q, %v", got, err)
	}

	// 不存在的文件
	if _, err := read.Execute(context.Background(), &ReadFileParams{Path: filepath.Join(dir, "missing.txt")}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("不存在的文件应报错")
	}
}

func TestReadFileBinaryRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin.dat")
	if err := os.WriteFile(path, []byte{0x4d, 0x5a, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	read, _, _, _, _ := newTestFileTools(t, dir)
	if _, err := read.Execute(context.Background(), &ReadFileParams{Path: path}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("二进制文件应拒绝读取")
	}
}

func TestWriteFileCreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	_, write, _, _, _ := newTestFileTools(t, dir)

	// 新建（含不存在的父目录）
	nested := filepath.Join(dir, "sub", "dir", "new.go")
	got, err := write.Execute(context.Background(), &WriteFileParams{Path: nested, Content: "package main\n"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("write_file 失败: %v", err)
	}
	if !strings.Contains(got, "新建文件") || !strings.Contains(got, "1 行") {
		t.Fatalf("新建反馈不符: %q", got)
	}
	data, _ := os.ReadFile(nested)
	if string(data) != "package main\n" {
		t.Fatalf("写入内容不符: %q", data)
	}

	// 覆盖
	got, err = write.Execute(context.Background(), &WriteFileParams{Path: nested, Content: "package main\n\nfunc main() {}\n"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("write_file 覆盖失败: %v", err)
	}
	if !strings.Contains(got, "覆盖已有文件") {
		t.Fatalf("覆盖反馈不符: %q", got)
	}
	data, _ = os.ReadFile(nested)
	if !strings.Contains(string(data), "func main()") {
		t.Fatalf("覆盖内容不符: %q", data)
	}

	// 敏感文件拒绝
	if _, err := write.Execute(context.Background(), &WriteFileParams{Path: filepath.Join(dir, "aniabot.db"), Content: "x"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("aniabot.db 应被拒绝")
	}
}

func TestWriteFileAppend(t *testing.T) {
	dir := t.TempDir()
	_, write, _, _, _ := newTestFileTools(t, dir)
	path := filepath.Join(dir, "long.go")

	// 分段写入：首段覆盖写，后续段追加
	if _, err := write.Execute(context.Background(), &WriteFileParams{Path: path, Content: "package main\n"}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("首段写入失败: %v", err)
	}
	got, err := write.Execute(context.Background(), &WriteFileParams{Path: path, Content: "\nfunc main() {}\n", Append: true}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("追加写入失败: %v", err)
	}
	if !strings.Contains(got, "已追加") {
		t.Fatalf("追加反馈不符: %q", got)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("分段写入的最终内容不符: %q", data)
	}

	// 追加到不存在的文件：创建并写入
	path2 := filepath.Join(dir, "sub", "fresh.txt")
	if _, err := write.Execute(context.Background(), &WriteFileParams{Path: path2, Content: "第一段\n", Append: true}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("对不存在文件追加失败: %v", err)
	}
	data, _ = os.ReadFile(path2)
	if string(data) != "第一段\n" {
		t.Fatalf("创建追加内容不符: %q", data)
	}
}

func TestEditFileUniqueReplace(t *testing.T) {
	dir := t.TempDir()
	_, _, edit, _, _ := newTestFileTools(t, dir)
	path := filepath.Join(dir, "code.go")
	original := "package main\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := edit.Execute(context.Background(), &EditFileParams{
		Path:      path,
		OldString: "return a + b",
		NewString: "return a + b // 相加",
	}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("edit_file 失败: %v", err)
	}
	if !strings.Contains(got, "已替换 1 处") {
		t.Fatalf("替换反馈不符: %q", got)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "return a + b // 相加") {
		t.Fatalf("替换未落盘: %q", data)
	}

	// 未找到
	if _, err := edit.Execute(context.Background(), &EditFileParams{
		Path: path, OldString: "不存在的原文", NewString: "x",
	}, llmtool.CallBackFuncs{}); err == nil || !strings.Contains(err.Error(), "未在") {
		t.Fatalf("未找到应报错并引导先读: %v", err)
	}

	// 不唯一且未开 replace_all
	multi := filepath.Join(dir, "multi.txt")
	os.WriteFile(multi, []byte("same\nsame\n"), 0o644)
	if _, err := edit.Execute(context.Background(), &EditFileParams{
		Path: multi, OldString: "same", NewString: "other",
	}, llmtool.CallBackFuncs{}); err == nil || !strings.Contains(err.Error(), "不唯一") {
		t.Fatalf("不唯一应报错: %v", err)
	}

	// replace_all
	got, err = edit.Execute(context.Background(), &EditFileParams{
		Path: multi, OldString: "same", NewString: "other", ReplaceAll: true,
	}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("replace_all 失败: %v", err)
	}
	if !strings.Contains(got, "已替换 2 处") {
		t.Fatalf("replace_all 计数不符: %q", got)
	}
	data, _ = os.ReadFile(multi)
	if string(data) != "other\nother\n" {
		t.Fatalf("replace_all 未落盘: %q", data)
	}

	// old == new 拒绝
	if _, err := edit.Execute(context.Background(), &EditFileParams{
		Path: path, OldString: "x", NewString: "x",
	}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("old_string 与 new_string 相同应报错")
	}
}

func TestGlobDoubleStar(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{
		"a.go", "sub/b.go", "sub/deep/c.go", "sub/readme.md",
	} {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("x"), 0o644)
	}
	_, _, _, glob, _ := newTestFileTools(t, dir)

	// **/*.go 应匹配三层
	got, err := glob.Execute(context.Background(), &GlobParams{Pattern: "**/*.go"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("glob 失败: %v", err)
	}
	for _, want := range []string{"a.go", filepath.Join("sub", "b.go"), filepath.Join("sub", "deep", "c.go")} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少匹配 %q: %q", want, got)
		}
	}
	if strings.Contains(got, "readme.md") {
		t.Fatalf("不应匹配 md: %q", got)
	}

	// 顶层 *.go 只匹配 a.go
	got, _ = glob.Execute(context.Background(), &GlobParams{Pattern: "*.go"}, llmtool.CallBackFuncs{})
	if !strings.Contains(got, "a.go") || strings.Contains(got, "b.go") {
		t.Fatalf("顶层模式匹配错误: %q", got)
	}

	// 无匹配
	got, _ = glob.Execute(context.Background(), &GlobParams{Pattern: "**/*.py"}, llmtool.CallBackFuncs{})
	if !strings.Contains(got, "未找到") {
		t.Fatalf("无匹配应提示: %q", got)
	}
}

func TestGrepSearch(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "pkg", "util.go"), []byte("package pkg\n\nfunc Hello() string {\n\treturn \"hello\"\n}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello world\n"), 0o644)
	_, _, _, _, grep := newTestFileTools(t, dir)

	// 全目录搜索
	got, err := grep.Execute(context.Background(), &GrepParams{Pattern: "hello"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("grep 失败: %v", err)
	}
	if !strings.Contains(got, "main.go:4:") || !strings.Contains(got, "util.go:4:") || !strings.Contains(got, "note.txt:1:") {
		t.Fatalf("匹配结果不符:\n%s", got)
	}

	// include 过滤
	got, _ = grep.Execute(context.Background(), &GrepParams{Pattern: "hello", Include: "*.go"}, llmtool.CallBackFuncs{})
	if strings.Contains(got, "note.txt") {
		t.Fatalf("include 过滤未生效:\n%s", got)
	}

	// 单文件搜索带行号
	got, _ = grep.Execute(context.Background(), &GrepParams{Pattern: "Hello", Path: filepath.Join(dir, "pkg", "util.go")}, llmtool.CallBackFuncs{})
	if !strings.Contains(got, "util.go:3:") {
		t.Fatalf("单文件搜索不符:\n%s", got)
	}

	// 无匹配
	got, _ = grep.Execute(context.Background(), &GrepParams{Pattern: "zzz_not_exist"}, llmtool.CallBackFuncs{})
	if !strings.Contains(got, "未找到") {
		t.Fatalf("无匹配应提示: %q", got)
	}

	// 非法正则
	if _, err := grep.Execute(context.Background(), &GrepParams{Pattern: "("}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("非法正则应报错")
	}
}

func TestFileToolsRootJail(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0o644)
	read, write, _, glob, _ := newTestFileTools(t, dir)

	// 绝对路径越界拒绝
	if _, err := read.Execute(context.Background(), &ReadFileParams{Path: outsideFile}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("越界绝对路径应被拒绝")
	}
	if _, err := write.Execute(context.Background(), &WriteFileParams{Path: outsideFile, Content: "x"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("越界写入应被拒绝")
	}
	if _, err := glob.Execute(context.Background(), &GlobParams{Pattern: "**/*", Path: outside}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("越界搜索根目录应被拒绝")
	}

	// 相对路径允许且基于 root
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("fine"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := read.Execute(context.Background(), &ReadFileParams{Path: "ok.txt"}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(got, "fine") {
		t.Fatalf("root 内相对路径读取失败: %q, %v", got, err)
	}

	// 相对路径穿越拒绝
	if _, err := read.Execute(context.Background(), &ReadFileParams{Path: filepath.Join("..", filepath.Base(outside), "secret.txt")}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal(".. 穿越应被拒绝")
	}
}

// TestFileToolsRootJailSymlink root jail 必须挡住符号链接逃逸：纯词法校验
// （Abs/Rel）挡不住 root 内一个指向外部的软链——read_file/write_file/edit_file/
// glob/grep 会顺着它读写 jail 之外的路径，而面板文案承诺"所有路径必须位于该
// 目录内"。目标不存在时对父目录复检，覆盖"写到软链目录下"的情形。
func TestFileToolsRootJailSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top-secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	// root 内建软链：link.txt -> <outside>/secret.txt
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("当前环境不支持符号链接: %v", err)
	}
	// root 内建指向外部目录的软链：outdir -> outside
	outDirLink := filepath.Join(dir, "outdir")
	if err := os.Symlink(outside, outDirLink); err != nil {
		t.Skipf("当前环境不支持目录符号链接: %v", err)
	}

	read, write, edit, glob, _ := newTestFileTools(t, dir)

	// 经软链读文件：必须拒绝
	if got, err := read.Execute(context.Background(), &ReadFileParams{Path: "link.txt"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatalf("经符号链接读取 jail 外文件应被拒绝, got %q", got)
	}
	// 经软链写文件：必须拒绝
	if _, err := write.Execute(context.Background(), &WriteFileParams{Path: "link.txt", Content: "pwned"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("经符号链接写入应被拒绝")
	}
	// 经目录软链读外部文件：必须拒绝
	if _, err := read.Execute(context.Background(), &ReadFileParams{Path: filepath.Join("outdir", "secret.txt")}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("经目录符号链接读取 jail 外文件应被拒绝")
	}
	// 经目录软链新建文件（目标不存在，走父目录复检）：必须拒绝
	if _, err := write.Execute(context.Background(), &WriteFileParams{Path: filepath.Join("outdir", "new.txt"), Content: "x"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("经目录符号链接写入新文件应被拒绝")
	}
	// 经软链 edit：必须拒绝
	if _, err := edit.Execute(context.Background(), &EditFileParams{Path: "link.txt", OldString: "top", NewString: "x"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("经符号链接编辑应被拒绝")
	}
	// 经目录软链 glob：必须拒绝
	if _, err := glob.Execute(context.Background(), &GlobParams{Pattern: "**/*", Path: "outdir"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("经目录符号链接搜索应被拒绝")
	}

	// 内容未被篡改
	if data, err := os.ReadFile(secret); err != nil || string(data) != "top-secret" {
		t.Fatalf("jail 外文件被改写: %q, %v", data, err)
	}
	// root 内普通文件仍可正常读写
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := read.Execute(context.Background(), &ReadFileParams{Path: "plain.txt"}, llmtool.CallBackFuncs{}); err != nil || !strings.Contains(got, "ok") {
		t.Fatalf("root 内普通文件应可读: %q, %v", got, err)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", false},
		{"**/*.go", "sub/main.go", true},
		{"**/*.go", "main.go", true},
		{"sub/**/*.md", "sub/a/b.md", true},
		{"sub/**/*.md", "sub/b.md", true},
		{"sub/*.md", "sub/a/b.md", false},
		{"a?c", "abc", true},          // ? 匹配单个字符
		{"a?c.txt", "abc.txt", true},  // ? 单字符
		{"**", "any/deep/file", true}, // ** 单独使用
		{"docs/**", "docs/x/y.md", true},
		// ** 密集 + 不匹配：递归实现会指数回溯，DP/memoized 实现应瞬间完成
		{"**/**/**/**/**/**/**/**/z", strings.Repeat("a/", 30) + "a", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.name); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// TestGlobSegMatchPathological * 密集的病态模式在递归回溯下指数爆炸；
// DP 实现应瞬间完成（回归守护，防止改回落溯实现）。
func TestGlobSegMatchPathological(t *testing.T) {
	pattern := strings.Repeat("*a", 60) + "b" // 无 b，需完整回溯后判负
	name := strings.Repeat("a", 200)
	start := time.Now()
	if globSegMatch(pattern, name) {
		t.Fatal("不应匹配")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("病态模式匹配耗时过长: %v", elapsed)
	}
}

// TestGlobPatternLengthCapped glob pattern 超长应直接报错而非尝试匹配。
func TestGlobPatternLengthCapped(t *testing.T) {
	_, _, _, glob, _ := newTestFileTools(t, t.TempDir())
	long := strings.Repeat("*", globPatternMaxLen+1)
	if _, err := glob.Execute(context.Background(), &GlobParams{Pattern: long}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("超长 pattern 应报错")
	}
}

// TestReadFileCRLFStripped CRLF 文件的行尾 \r 应从 read_file 展示中剥离。
func TestReadFileCRLFStripped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "win.txt")
	if err := os.WriteFile(path, []byte("line1\r\nline2\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read, _, _, _, _ := newTestFileTools(t, dir)
	got, err := read.Execute(context.Background(), &ReadFileParams{Path: path}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("read_file 失败: %v", err)
	}
	if strings.Contains(got, "\r") {
		t.Fatalf("行尾 \\r 应从展示中剥离: %q", got)
	}
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line2") {
		t.Fatalf("内容缺失: %q", got)
	}
}

// TestEditFileCRLFFile CRLF 文件：模型按 read_file 所见（LF）构造 old_string，
// edit_file 应回退按 CRLF 匹配完成替换，且文件换行风格保持不变。
func TestEditFileCRLFFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "win.go")
	original := "package main\r\n\r\nfunc main() {\r\n\tprintln(1)\r\n}\r\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, edit, _, _ := newTestFileTools(t, dir)
	got, err := edit.Execute(context.Background(), &EditFileParams{
		Path:      path,
		OldString: "func main() {\n\tprintln(1)\n}",
		NewString: "func main() {\n\tprintln(2)\n}",
	}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("CRLF 回退匹配失败: %v", err)
	}
	data, _ := os.ReadFile(path)
	// 文件保持 CRLF，仅替换内容落盘
	if want := "package main\r\n\r\nfunc main() {\r\n\tprintln(2)\r\n}\r\n"; string(data) != want {
		t.Fatalf("替换结果不符:\ngot:  %q\nwant: %q", string(data), want)
	}
	// 5 个文本行（off-by-one 回归：此前误报 6 行）
	if !strings.Contains(got, "已替换 1 处") || !strings.Contains(got, "共 5 行") {
		t.Fatalf("替换反馈不符: %q", got)
	}
}
