package aitool

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// fakeQQ 测试桩：仅实现用到的 QQExt 方法（未用方法走嵌入接口，调用即 panic，
// 恰好可用于断言工具不会误调）。
type fakeQQ struct {
	adapter.QQExt
	groups     []message.GroupInfo
	friends    []message.Friend
	userInfo   *message.GroupUserInfo
	members    []message.GroupUserInfo
	characters []message.AICharacterGroup
	charGroup  message.QID

	voiceGroup message.QID
	voiceChar  string
	voiceText  string
	voiceOK    bool

	pokeUser  message.QID
	pokeGroup *message.QID
	pokeOK    bool

	signGroup message.QID
	signOK    bool
}

func (f *fakeQQ) GetGroupList() (*[]message.GroupInfo, bool) { return &f.groups, true }
func (f *fakeQQ) GetFriendList() (*[]message.Friend, bool)   { return &f.friends, true }
func (f *fakeQQ) GetGroupUserInfo(groupId, userId message.QID) (*message.GroupUserInfo, bool) {
	return f.userInfo, true
}
func (f *fakeQQ) GetGroupMemberList(groupId message.QID, noCache bool) (*[]message.GroupUserInfo, bool) {
	return &f.members, true
}
func (f *fakeQQ) GetAICharacters(groupId message.QID) (*[]message.AICharacterGroup, bool) {
	f.charGroup = groupId
	return &f.characters, true
}
func (f *fakeQQ) SendGroupAIVoiceMsg(groupId message.QID, character, msg string) (message.QID, bool) {
	f.voiceGroup, f.voiceChar, f.voiceText = groupId, character, msg
	return "qq:10001", f.voiceOK
}
func (f *fakeQQ) SendPokeMsg(userId message.QID, groupId *message.QID) bool {
	f.pokeUser, f.pokeGroup = userId, groupId
	return f.pokeOK
}
func (f *fakeQQ) SendGroupSign(groupId message.QID) bool {
	f.signGroup = groupId
	return f.signOK
}

func groupCtx() Context {
	return Context{Target: message.QID("qq:888"), IsGroup: true}
}

// TestQQToolsOrderFixed 工具切片顺序固定且名字唯一：注入工具会序列化进每次
// LLM 请求的 tools 字段，任何顺序抖动都会打失上游 prompt 前缀缓存。
func TestQQToolsOrderFixed(t *testing.T) {
	want := []string{
		"qq_get_group_list", "qq_get_friend_list", "qq_get_group_user_info",
		"qq_get_group_member_list", "qq_get_ai_characters", "qq_get_msg_history",
		"qq_send_ai_voice", "qq_send_poke", "qq_send_group_sign",
	}
	for i := 0; i < 2; i++ {
		tools := QQTools(groupCtx(), &fakeQQ{})
		if len(tools) != len(want) {
			t.Fatalf("工具数量不符: got %d want %d", len(tools), len(want))
		}
		seen := map[string]struct{}{}
		for j, tool := range tools {
			if tool.Name() != want[j] {
				t.Fatalf("第 %d 个工具名不符: got %q want %q", j, tool.Name(), want[j])
			}
			if _, dup := seen[tool.Name()]; dup {
				t.Fatalf("工具名重复: %s", tool.Name())
			}
			seen[tool.Name()] = struct{}{}
		}
	}
}

// TestQQSideEffectFlags 副作用声明只落在 qq_send_* 三个写工具上，
// 计划模式门禁据此阻断（见 pluginaichat.platformToolBlocked）。
func TestQQSideEffectFlags(t *testing.T) {
	sideEffect := map[string]bool{
		"qq_send_ai_voice": true, "qq_send_poke": true, "qq_send_group_sign": true,
	}
	for _, tool := range QQTools(groupCtx(), &fakeQQ{}) {
		se, ok := tool.(SideEffector)
		want := sideEffect[tool.Name()]
		if want {
			if !ok || !se.SideEffect() {
				t.Errorf("%s 应声明副作用", tool.Name())
			}
			continue
		}
		if ok && se.SideEffect() {
			t.Errorf("%s 不应声明副作用", tool.Name())
		}
	}
}

func TestNormalizeID(t *testing.T) {
	cases := []struct {
		input    string
		template message.QID
		want     message.QID
		wantErr  bool
	}{
		{"123456", "qq:888", "qq:123456", false},
		{"qq:123456", "qq:888", "qq:123456", false},
		{"lil:123456", "qq:888", "qq:123456", false}, // 跨协议端写法按会话前缀还原
		{"456", "lil:888", "lil:456", false},
		{"789", "888", "789", false}, // 无前缀的裸数字平台
		{"abc", "qq:888", "", true},
		{"", "qq:888", "", true},
		{"  qq:123  ", "qq:888", "qq:123", false},
	}
	for _, c := range cases {
		got, err := NormalizeID(c.input, c.template)
		if c.wantErr {
			if err == nil {
				t.Errorf("NormalizeID(%q,%q) 应报错，got %q", c.input, c.template, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeID(%q,%q) 意外报错: %v", c.input, c.template, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeID(%q,%q) = %q, want %q", c.input, c.template, got, c.want)
		}
	}
}

func TestGroupListTool(t *testing.T) {
	fake := &fakeQQ{groups: []message.GroupInfo{
		{GroupID: "qq:2", GroupName: "B群", MemberCount: 10},
		{GroupID: "qq:1", GroupName: "A群", MemberCount: 5},
	}}
	tool := &qqGroupListTool{qqBase{qq: fake, target: "qq:888", isGroup: true}}
	out, err := tool.Execute(context.Background(), &struct{}{})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	// 输出按群号排序，保证同一数据多次调用结果一致
	if strings.Index(out, "qq:1") > strings.Index(out, "qq:2") {
		t.Errorf("群列表应按群号排序:\n%s", out)
	}
	if !strings.Contains(out, "共 2 个群") {
		t.Errorf("缺少统计行:\n%s", out)
	}
}

func TestGroupUserInfoToolDefaults(t *testing.T) {
	fake := &fakeQQ{userInfo: &message.GroupUserInfo{
		UserID: "qq:123", Nickname: "张三", Card: "三哥", Role: "admin",
		JoinTime: 1700000000, LastSentTime: 1700001000,
	}}
	// 群会话缺省 group_id → 自动回退会话群
	tool := &qqGroupUserInfoTool{qqBase{qq: fake, target: "qq:888", isGroup: true}}
	out, err := tool.Execute(context.Background(), &qqGroupUserInfoParams{UserID: "qq:123"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "三哥") || !strings.Contains(out, "admin") {
		t.Errorf("输出缺少关键字段:\n%s", out)
	}
	// formatUnix 按本地时区展示，期望日期动态计算，避免时区相关断言在 UTC 环境失败
	wantDate := time.Unix(1700000000, 0).Format("2006-01-02")
	if !strings.Contains(out, wantDate) {
		t.Errorf("入群时间未格式化为 %s:\n%s", wantDate, out)
	}

	// 私聊会话缺省 group_id → 报错提示显式指定
	tool = &qqGroupUserInfoTool{qqBase{qq: fake, target: "qq:123", isGroup: false}}
	if _, err := tool.Execute(context.Background(), &qqGroupUserInfoParams{UserID: "qq:123"}); err == nil {
		t.Fatal("私聊会话缺省 group_id 应报错")
	}
	// 私聊会话显式群号 → 正常
	if _, err := tool.Execute(context.Background(), &qqGroupUserInfoParams{UserID: "123", GroupID: "888"}); err != nil {
		t.Fatalf("私聊会话显式群号应成功: %v", err)
	}
}

func TestMemberListTruncated(t *testing.T) {
	members := make([]message.GroupUserInfo, 0, qqMemberListMax+50)
	for i := 0; i < qqMemberListMax+50; i++ {
		members = append(members, message.GroupUserInfo{UserID: message.QID("qq:" + strconv.Itoa(i)), Nickname: "u"})
	}
	fake := &fakeQQ{members: members}
	tool := &qqGroupMemberListTool{qqBase{qq: fake, target: "qq:888", isGroup: true}}
	out, err := tool.Execute(context.Background(), &qqGroupMemberListParams{})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "共 250 名成员") || !strings.Contains(out, "前 200 名") {
		t.Errorf("截断提示缺失:\n%s", out[:100])
	}
}

func TestSendAIVoiceAutoCharacter(t *testing.T) {
	fake := &fakeQQ{
		characters: []message.AICharacterGroup{{
			Type:       "official",
			Characters: []message.AICharacter{{CharacterID: "ch-1", CharacterName: "小雨"}},
		}},
		voiceOK: true,
	}
	tool := &qqSendAIVoiceTool{qqBase{qq: fake, target: "qq:888", isGroup: true}}
	out, err := tool.Execute(context.Background(), &qqSendAIVoiceParams{Text: "你好呀"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if fake.voiceChar != "ch-1" || fake.voiceGroup != "qq:888" || fake.voiceText != "你好呀" {
		t.Errorf("参数传递不符: group=%s char=%s text=%s", fake.voiceGroup, fake.voiceChar, fake.voiceText)
	}
	if !strings.Contains(out, "已发送") {
		t.Errorf("输出缺少确认:\n%s", out)
	}

	// 角色列表不可用时要求显式指定 character
	fake2 := &fakeQQ{voiceOK: true}
	tool2 := &qqSendAIVoiceTool{qqBase{qq: fake2, target: "qq:888", isGroup: true}}
	if _, err := tool2.Execute(context.Background(), &qqSendAIVoiceParams{Text: "hi"}); err == nil {
		t.Fatal("角色列表不可用且未指定 character 应报错")
	}
}

// TestAICharactersPerGroup 角色列表按群查询：群聊省略 group_id 时用当前群，
// 私聊必须显式指定；返回的分组列表展平成「ID 名称」行。
func TestAICharactersPerGroup(t *testing.T) {
	fake := &fakeQQ{characters: []message.AICharacterGroup{
		{Type: "official", Characters: []message.AICharacter{{CharacterID: "ch-1", CharacterName: "小雨"}}},
		{Type: "clone", Characters: []message.AICharacter{{CharacterID: "ch-2", CharacterName: "小柔"}}},
	}}
	tool := &qqAICharactersTool{qqBase{qq: fake, target: "qq:888", isGroup: true}}
	out, err := tool.Execute(context.Background(), &qqAICharactersParams{})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if fake.charGroup != "qq:888" {
		t.Errorf("群聊会话应默认查询当前群，got %q", fake.charGroup)
	}
	for _, want := range []string{"共 2 个可用角色", "ch-1 小雨", "ch-2 小柔"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}

	// 私聊未指定群号 → 直接报错，不调协议端
	fake2 := &fakeQQ{}
	tool2 := &qqAICharactersTool{qqBase{qq: fake2, target: "qq:123", isGroup: false}}
	if _, err := tool2.Execute(context.Background(), &qqAICharactersParams{}); err == nil {
		t.Fatal("私聊未指定 group_id 应报错")
	}
	if fake2.charGroup != "" {
		t.Errorf("参数校验失败时不应调用协议端，got %q", fake2.charGroup)
	}
}

func TestSendPokeGroupAndFriend(t *testing.T) {
	// 群会话缺省群号 → 群戳
	fake := &fakeQQ{pokeOK: true}
	tool := &qqSendPokeTool{qqBase{qq: fake, target: "qq:888", isGroup: true}}
	if _, err := tool.Execute(context.Background(), &qqSendPokeParams{UserID: "qq:123"}); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if fake.pokeUser != "qq:123" || fake.pokeGroup == nil || *fake.pokeGroup != "qq:888" {
		t.Errorf("群戳参数不符: user=%s group=%v", fake.pokeUser, fake.pokeGroup)
	}

	// 私聊会话省略群号 → 好友戳（群指针为 nil）
	fake2 := &fakeQQ{pokeOK: true}
	tool2 := &qqSendPokeTool{qqBase{qq: fake2, target: "qq:123", isGroup: false}}
	if _, err := tool2.Execute(context.Background(), &qqSendPokeParams{UserID: "qq:123"}); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if fake2.pokeGroup != nil {
		t.Errorf("好友戳不应携带群号: %v", *fake2.pokeGroup)
	}

	// 协议端失败 → 报错
	fake3 := &fakeQQ{pokeOK: false}
	tool3 := &qqSendPokeTool{qqBase{qq: fake3, target: "qq:888", isGroup: true}}
	if _, err := tool3.Execute(context.Background(), &qqSendPokeParams{UserID: "1"}); err == nil {
		t.Fatal("发送失败应报错")
	}
}

func TestSendGroupSign(t *testing.T) {
	fake := &fakeQQ{signOK: true}
	tool := &qqSendGroupSignTool{qqBase{qq: fake, target: "qq:888", isGroup: true}}
	out, err := tool.Execute(context.Background(), &qqSendGroupSignParams{})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if fake.signGroup != "qq:888" || !strings.Contains(out, "完成打卡") {
		t.Errorf("打卡结果不符: group=%s out=%s", fake.signGroup, out)
	}
}
