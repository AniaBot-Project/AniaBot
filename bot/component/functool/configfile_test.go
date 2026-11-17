package functool

import (
	"context"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

func TestConfigFileGetList(t *testing.T) {
	tool := NewConfigFileGetTool(newFakeConfigStore())

	// 留空 name：列出全部文件与格式说明
	out, err := tool.Execute(context.Background(), &ConfigFileGetParams{}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	for _, name := range []string{"mcp", "prompt", "hooks", "commands"} {
		if !strings.Contains(out, "- "+name+":") {
			t.Fatalf("列表缺少 %s: %s", name, out)
		}
	}

	// 未知文件名报错
	if _, err := tool.Execute(context.Background(), &ConfigFileGetParams{Name: "bogus"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("未知文件名应报错")
	}
}

func TestConfigFileGetContent(t *testing.T) {
	store := newFakeConfigStore()
	store.data["files.hooks_json"] = `{"hooks":{"PreToolUse":[{"command":"echo hi"}]}}`
	tool := NewConfigFileGetTool(store)

	// 读取已有内容
	out, err := tool.Execute(context.Background(), &ConfigFileGetParams{Name: "hooks"}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "files.hooks_json") || !strings.Contains(out, "echo hi") {
		t.Fatalf("未返回文件内容: %s", out)
	}

	// 空文件返回格式说明
	if out, err := tool.Execute(context.Background(), &ConfigFileGetParams{Name: "mcp"}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("Execute error: %v", err)
	} else if !strings.Contains(out, "servers") {
		t.Fatalf("空文件应返回格式说明: %s", out)
	}
}

func TestConfigFileSet(t *testing.T) {
	store := newFakeConfigStore()
	tool := NewConfigFileSetTool(store)

	// 写入合法 JSON
	if _, err := tool.Execute(context.Background(), &ConfigFileSetParams{Name: "commands", Content: `{"commands":{"hi":"你好"}}`}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if v, _ := store.Get("files.commands_json"); v != `{"commands":{"hi":"你好"}}` {
		t.Fatalf("写入内容异常: %#v", v)
	}

	// 空内容可写入（等价面板清空）
	if _, err := tool.Execute(context.Background(), &ConfigFileSetParams{Name: "hooks", Content: "   "}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("空内容应允许写入: %v", err)
	}

	// 非法 JSON 拒绝
	if _, err := tool.Execute(context.Background(), &ConfigFileSetParams{Name: "hooks", Content: `{"hooks":`}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("非法 JSON 应拒绝写入")
	}

	// 未知文件名报错
	if _, err := tool.Execute(context.Background(), &ConfigFileSetParams{Name: "bogus", Content: "{}"}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("未知文件名应报错")
	}

	// 生效时机提示与面板一致：prompt/hooks/commands 热重读，mcp 需重启
	if out, err := tool.Execute(context.Background(), &ConfigFileSetParams{Name: "prompt", Content: `{"groups":{}}`}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("Execute error: %v", err)
	} else if strings.Contains(out, "重启后生效") || !strings.Contains(out, "数秒内生效") {
		t.Fatalf("prompt 保存后应提示数秒内生效，got: %s", out)
	}
	if out, err := tool.Execute(context.Background(), &ConfigFileSetParams{Name: "mcp", Content: `{"servers":[]}`}, llmtool.CallBackFuncs{}); err != nil {
		t.Fatalf("Execute error: %v", err)
	} else if !strings.Contains(out, "重启后生效") {
		t.Fatalf("mcp 保存后应提示重启生效，got: %s", out)
	}
}
