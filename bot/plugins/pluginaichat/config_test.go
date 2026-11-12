package pluginaichat

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/common/pluginconfig"
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
		"skill_read",
		"web 技能",
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

// TestProactiveActivitySliderSchema 主动互动的节奏配置已收敛为一个「活跃度」滑条：
// 面板只应出现滑条本身，旧的逐项数字不应再注册。
func TestProactiveActivitySliderSchema(t *testing.T) {
	var cfg aiChatConfig
	if err := pluginconfig.RegisterStruct(&cfg); err != nil {
		t.Fatalf("RegisterStruct: %v", err)
	}
	byKey := map[string]pluginconfig.Field{}
	for _, f := range pluginconfig.Fields() {
		byKey[f.Key] = f
	}

	act, ok := byKey["plugin.ai_chat_bot.proactive.activity"]
	if !ok {
		t.Fatal("主动互动缺少 activity 滑条")
	}
	if act.Type != "slider" || act.Min != 0 || act.Max != 100 || act.Step != 5 {
		t.Errorf("activity 字段 = %+v", act)
	}
	if want := []string{"冷淡", "克制", "均衡", "活跃", "话痨"}; !reflect.DeepEqual(act.Ticks, want) {
		t.Errorf("activity 刻度 = %v, 期望 %v", act.Ticks, want)
	}
	if act.Default != int64(proactiveDefaultActivity) {
		t.Errorf("activity 默认值 = %#v, 期望 %d", act.Default, proactiveDefaultActivity)
	}

	// 收敛掉的逐项节奏参数：留在数据库里不影响运行，但面板不应再出现
	for _, key := range []string{
		"idle_minutes", "min_gap_minutes", "daily_limit", "global_daily_limit",
		"expire_minutes", "outcome_minutes", "cooldown_minutes", "strike_limit",
	} {
		if _, ok := byKey["plugin.ai_chat_bot.proactive."+key]; ok {
			t.Errorf("节奏参数 %s 应已并入活跃度滑条", key)
		}
	}
	// 免打扰与「高级」两项保留，且主动互动的全部字段落在同一个面板板块：
	// 面板只支持两级分组，此前的「主动互动 · 高级」会被拆成独立板块，已合并。
	const wantGroup = "AI 对话 · 主动互动"
	for _, key := range []string{
		"enable", "activity", "scopes", "quiet_hours", "timeout_sec", "max_context_messages",
	} {
		field, ok := byKey["plugin.ai_chat_bot.proactive."+key]
		if !ok {
			t.Errorf("配置项 %s 不应被移除", key)
			continue
		}
		if field.Group != wantGroup {
			t.Errorf("配置项 %s 分组 = %q, 期望 %q（应与其它主动互动配置同处一个板块）", key, field.Group, wantGroup)
		}
	}
}
