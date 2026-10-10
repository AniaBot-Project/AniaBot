package pluginaichat

import (
	"reflect"
	"strings"
	"testing"
)

func TestDefaultPromptConfigTagMatchesConstant(t *testing.T) {
	field, ok := reflect.TypeFor[aiChatConfig]().FieldByName("Prompt")
	if !ok {
		t.Fatal("aiChatConfig.Prompt 字段不存在")
	}
	raw, ok := field.Tag.Lookup("default")
	if !ok {
		t.Fatal("Prompt 字段缺少 default 标签")
	}

	if raw != defaultPrompt {
		t.Fatal("Prompt 字段 default 标签与 defaultPrompt 常量不一致")
	}
}

func TestDefaultPromptContainsToolGuidance(t *testing.T) {
	tools := []string{
		"get_msg_history",
		"load_images",
		"webSearch",
		"webExplore",
		"memory_search",
		"memory_save",
		"memory_update",
		"clock_create",
		"subagent_run",
		"team_run",
	}
	for _, tool := range tools {
		if !strings.Contains(defaultPrompt, tool) {
			t.Errorf("defaultPrompt 未包含工具 %s 的使用指引", tool)
		}
	}
}
