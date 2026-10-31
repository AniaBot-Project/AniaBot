package aitool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/common/adapter"
	"github.com/jeanhua/AniaBot/common/model/message"
)

// 本文件实现 QQ 平台（OneBot v11 协议端：NapCat / Luckylilia）的 AI 工具集，
// 全部基于 adapter.QQExt 可选能力构造，供两个协议端的 bot 外观共用：
//
//	qqBot/lilBot.AITools(ctx) → QQTools(ctx, qq)
//
// 工具名统一 qq_ 前缀；读工具在前、副作用工具在后，构造顺序固定。
// 协议端未实现的能力（接口返回失败）在工具结果中如实提示，不猜测原因。

// QQTools 构造 QQ 平台注入会话的工具列表（顺序固定：读工具在前、副作用工具在后）。
func QQTools(ctx Context, qq adapter.QQExt) []Tool {
	base := qqBase{qq: qq, target: ctx.Target, isGroup: ctx.IsGroup}
	return []Tool{
		&qqGroupListTool{base},
		&qqFriendListTool{base},
		&qqGroupUserInfoTool{base},
		&qqGroupMemberListTool{base},
		&qqAICharactersTool{base},
		&msgHistoryTool{ctx: ctx, name: "qq_get_msg_history"},
		&qqSendAIVoiceTool{base},
		&qqSendPokeTool{base},
		&qqSendGroupSignTool{base},
	}
}

// qqBase QQ 工具的公共绑定：协议端能力 + 会话定位。
type qqBase struct {
	qq      adapter.QQExt
	target  message.QID // 会话对端 ID（群号 / 好友 ID）
	isGroup bool
}

// resolveGroup 解析目标群：参数缺省时回退当前会话群；私聊会话未显式传群号时报错。
func (b qqBase) resolveGroup(groupParam string) (message.QID, error) {
	if strings.TrimSpace(groupParam) != "" {
		return NormalizeID(groupParam, b.target)
	}
	if b.isGroup {
		return b.target, nil
	}
	return "", fmt.Errorf("当前为私聊会话，请显式指定 group_id")
}

// resolveUser 解析用户 ID 参数（必填）。
func (b qqBase) resolveUser(userParam string) (message.QID, error) {
	return NormalizeID(userParam, b.target)
}

// ─────────────────────────────────────────────
// 只读工具
// ─────────────────────────────────────────────

type qqGroupIDParams struct {
	GroupID string `json:"group_id,omitempty" desc:"目标群号；群聊会话可省略（默认当前群），私聊会话必须填写"`
}

type qqGroupListTool struct{ qqBase }

func (t *qqGroupListTool) Name() string { return "qq_get_group_list" }
func (t *qqGroupListTool) Description() string {
	return "获取本协议端 QQ 账号已加入的群聊列表（群号、群名、成员数）"
}
func (t *qqGroupListTool) Params() any { return &struct{}{} }
func (t *qqGroupListTool) Execute(ctx context.Context, params any) (string, error) {
	groups, ok := t.qq.GetGroupList()
	if !ok || groups == nil {
		return "", fmt.Errorf("获取群列表失败（协议端不支持或未登录）")
	}
	list := *groups
	sort.Slice(list, func(i, j int) bool { return list[i].GroupID < list[j].GroupID })
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("共 %d 个群：\n", len(list)))
	for _, g := range list {
		sb.WriteString(fmt.Sprintf("%s %s（成员 %d 人）\n", g.GroupID, g.GroupName, g.MemberCount))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

type qqFriendListTool struct{ qqBase }

func (t *qqFriendListTool) Name() string { return "qq_get_friend_list" }
func (t *qqFriendListTool) Description() string {
	return "获取本协议端 QQ 账号的好友列表（ID、昵称、备注）"
}
func (t *qqFriendListTool) Params() any { return &struct{}{} }
func (t *qqFriendListTool) Execute(ctx context.Context, params any) (string, error) {
	friends, ok := t.qq.GetFriendList()
	if !ok || friends == nil {
		return "", fmt.Errorf("获取好友列表失败（协议端不支持或未登录）")
	}
	list := *friends
	sort.Slice(list, func(i, j int) bool { return list[i].UserID < list[j].UserID })
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("共 %d 个好友：\n", len(list)))
	for _, f := range list {
		if f.Remark != "" && f.Remark != f.Nickname {
			sb.WriteString(fmt.Sprintf("%s %s（备注：%s）\n", f.UserID, f.Nickname, f.Remark))
		} else {
			sb.WriteString(fmt.Sprintf("%s %s\n", f.UserID, f.Nickname))
		}
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

type qqGroupUserInfoParams struct {
	UserID  string `json:"user_id" desc:"要查询的成员 ID（消息开头 [nickname:昵称 id:用户ID] 中的 id）"`
	GroupID string `json:"group_id,omitempty" desc:"所在群号；群聊会话可省略（默认当前群），私聊会话必须填写"`
}

type qqGroupUserInfoTool struct{ qqBase }

func (t *qqGroupUserInfoTool) Name() string { return "qq_get_group_user_info" }
func (t *qqGroupUserInfoTool) Description() string {
	return "查询成员在 QQ 群内的信息：群名片/昵称、群身份（群主/管理员/成员）、等级、入群时间、最后发言时间、头衔、是否被禁言等"
}
func (t *qqGroupUserInfoTool) Params() any { return &qqGroupUserInfoParams{} }
func (t *qqGroupUserInfoTool) Execute(ctx context.Context, params any) (string, error) {
	p := params.(*qqGroupUserInfoParams)
	groupID, err := t.resolveGroup(p.GroupID)
	if err != nil {
		return "", err
	}
	userID, err := t.resolveUser(p.UserID)
	if err != nil {
		return "", err
	}
	info, ok := t.qq.GetGroupUserInfo(groupID, userID)
	if !ok || info == nil {
		return "", fmt.Errorf("获取群成员信息失败（成员可能不在群 %s 中）", groupID)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("群 %s 成员 %s：\n", info.GroupID, info.UserID))
	nickname := info.Nickname
	if info.Card != "" {
		nickname = fmt.Sprintf("%s（群名片：%s）", info.Nickname, info.Card)
	}
	sb.WriteString("昵称：" + nickname + "\n")
	role := info.Role
	if role == "" {
		role = "member"
	}
	sb.WriteString("身份：" + role + "\n")
	if info.Level != "" {
		sb.WriteString("等级：" + info.Level + "\n")
	}
	if info.Title != "" {
		sb.WriteString("头衔：" + info.Title + "\n")
	}
	if info.Area != "" {
		sb.WriteString("地区：" + info.Area + "\n")
	}
	sb.WriteString("入群时间：" + formatUnix(info.JoinTime) + "\n")
	sb.WriteString("最后发言：" + formatUnix(info.LastSentTime) + "\n")
	if info.ShutUpTimestamp > 0 {
		remain := time.Until(time.Unix(int64(info.ShutUpTimestamp), 0)).Truncate(time.Second)
		sb.WriteString(fmt.Sprintf("禁言至：%s（剩余 %s）\n", formatUnix(info.ShutUpTimestamp), remain))
	}
	if info.IsRobot {
		sb.WriteString("该成员为机器人\n")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

const qqMemberListMax = 200

type qqGroupMemberListParams struct {
	GroupID string `json:"group_id,omitempty" desc:"目标群号；群聊会话可省略（默认当前群），私聊会话必须填写"`
	NoCache bool   `json:"no_cache,omitempty" desc:"true 时绕过协议端缓存重新拉取，默认 false"`
}

type qqGroupMemberListTool struct{ qqBase }

func (t *qqGroupMemberListTool) Name() string { return "qq_get_group_member_list" }
func (t *qqGroupMemberListTool) Description() string {
	return "获取 QQ 群的成员列表（ID、昵称/群名片、身份、等级）；大群只返回前 " +
		fmt.Sprint(qqMemberListMax) + " 名，查具体成员请优先用 qq_get_group_user_info"
}
func (t *qqGroupMemberListTool) Params() any { return &qqGroupMemberListParams{} }
func (t *qqGroupMemberListTool) Execute(ctx context.Context, params any) (string, error) {
	p := params.(*qqGroupMemberListParams)
	groupID, err := t.resolveGroup(p.GroupID)
	if err != nil {
		return "", err
	}
	members, ok := t.qq.GetGroupMemberList(groupID, p.NoCache)
	if !ok || members == nil {
		return "", fmt.Errorf("获取群成员列表失败")
	}
	list := *members
	total := len(list)
	sort.Slice(list, func(i, j int) bool { return list[i].UserID < list[j].UserID })
	truncated := false
	if total > qqMemberListMax {
		list = list[:qqMemberListMax]
		truncated = true
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("群 %s 共 %d 名成员", groupID, total))
	if truncated {
		sb.WriteString(fmt.Sprintf("（按 ID 排序显示前 %d 名）", qqMemberListMax))
	}
	sb.WriteString("：\n")
	for _, m := range list {
		name := m.Nickname
		if m.Card != "" {
			name = m.Card
		}
		role := m.Role
		if role == "" {
			role = "member"
		}
		sb.WriteString(fmt.Sprintf("%s %s（%s）\n", m.UserID, name, role))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// firstAICharacterID 取第一个分组里的第一个角色 ID（协议端按类别分组返回）。
func firstAICharacterID(groups []message.AICharacterGroup) string {
	for _, g := range groups {
		if len(g.Characters) > 0 {
			return g.Characters[0].CharacterID
		}
	}
	return ""
}

type qqAICharactersParams struct {
	GroupID string `json:"group_id,omitempty" desc:"目标群号；群聊会话可省略（默认当前群），私聊会话必须填写（AI 语音角色按群提供）"`
}

type qqAICharactersTool struct{ qqBase }

func (t *qqAICharactersTool) Name() string { return "qq_get_ai_characters" }
func (t *qqAICharactersTool) Description() string {
	return "获取指定群可用的 QQ AI 语音角色列表（供 qq_send_ai_voice 的 character 参数选择）"
}
func (t *qqAICharactersTool) Params() any { return &qqAICharactersParams{} }
func (t *qqAICharactersTool) Execute(ctx context.Context, params any) (string, error) {
	p, _ := params.(*qqAICharactersParams)
	var groupParam string
	if p != nil {
		groupParam = p.GroupID
	}
	groupID, err := t.resolveGroup(groupParam)
	if err != nil {
		return "", fmt.Errorf("AI 语音角色按群提供：%w", err)
	}
	groups, ok := t.qq.GetAICharacters(groupID)
	if !ok || groups == nil {
		return "", fmt.Errorf("获取群 %s 的 AI 语音角色列表失败（协议端未返回数据，可能未开启 AI 语音）", groupID)
	}
	list := *groups
	total := 0
	for _, g := range list {
		total += len(g.Characters)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("群 %s 共 %d 个可用角色：\n", groupID, total))
	for _, g := range list {
		for _, c := range g.Characters {
			sb.WriteString(fmt.Sprintf("%s %s\n", c.CharacterID, c.CharacterName))
		}
	}
	if total == 0 {
		sb.WriteString("（协议端返回空列表：该群可能未开放 AI 语音）\n")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// ─────────────────────────────────────────────
// 副作用工具（实现 SideEffector，计划模式下被阻断）
// ─────────────────────────────────────────────

type qqSendAIVoiceParams struct {
	Text      string `json:"text" desc:"要转为 AI 语音发送的文本内容"`
	Character string `json:"character,omitempty" desc:"语音角色 ID（可用 qq_get_ai_characters 查询）；省略时自动使用第一个可用角色"`
	GroupID   string `json:"group_id,omitempty" desc:"目标群号；群聊会话可省略（默认当前群），私聊会话必须填写（AI 语音仅支持群聊）"`
}

type qqSendAIVoiceTool struct{ qqBase }

func (t *qqSendAIVoiceTool) Name() string { return "qq_send_ai_voice" }
func (t *qqSendAIVoiceTool) Description() string {
	return "在 QQ 群中发送一条 AI 语音消息（把文本用指定角色音色合成后发出）；仅群聊可用，character 可先用 qq_get_ai_characters 查询"
}
func (t *qqSendAIVoiceTool) Params() any      { return &qqSendAIVoiceParams{} }
func (t *qqSendAIVoiceTool) SideEffect() bool { return true }
func (t *qqSendAIVoiceTool) Execute(ctx context.Context, params any) (string, error) {
	p := params.(*qqSendAIVoiceParams)
	if strings.TrimSpace(p.Text) == "" {
		return "", fmt.Errorf("text 不能为空")
	}
	groupID, err := t.resolveGroup(p.GroupID)
	if err != nil {
		return "", fmt.Errorf("AI 语音仅支持群聊：%w", err)
	}
	character := strings.TrimSpace(p.Character)
	if character == "" {
		groups, ok := t.qq.GetAICharacters(groupID)
		if !ok || groups == nil {
			return "", fmt.Errorf("未指定 character 且获取群 %s 的可用角色列表失败，请先用 qq_get_ai_characters 查询后显式指定", groupID)
		}
		character = firstAICharacterID(*groups)
		if character == "" {
			return "", fmt.Errorf("未指定 character 且群 %s 没有可用角色，请先用 qq_get_ai_characters 查询后显式指定", groupID)
		}
	}
	msgID, ok := t.qq.SendGroupAIVoiceMsg(groupID, character, p.Text)
	if !ok {
		return "", fmt.Errorf("发送 AI 语音失败（角色 %s 可能不可用）", character)
	}
	if msgID != "" {
		return fmt.Sprintf("AI 语音已发送到群 %s（角色 %s，消息 ID %s）", groupID, character, msgID), nil
	}
	return fmt.Sprintf("AI 语音已发送到群 %s（角色 %s）", groupID, character), nil
}

type qqSendPokeParams struct {
	UserID  string `json:"user_id" desc:"要戳的成员 ID（消息开头 [nickname:昵称 id:用户ID] 中的 id）"`
	GroupID string `json:"group_id,omitempty" desc:"所在群号；群聊会话可省略（默认当前群）。省略且当前为私聊会话时执行好友戳一戳"`
}

type qqSendPokeTool struct{ qqBase }

func (t *qqSendPokeTool) Name() string { return "qq_send_poke" }
func (t *qqSendPokeTool) Description() string {
	return "戳一戳指定成员（群内双击头像效果）；私聊会话省略 group_id 时对好友发起戳一戳。属玩闹性质动作，仅在用户明确要求时使用"
}
func (t *qqSendPokeTool) Params() any      { return &qqSendPokeParams{} }
func (t *qqSendPokeTool) SideEffect() bool { return true }
func (t *qqSendPokeTool) Execute(ctx context.Context, params any) (string, error) {
	p := params.(*qqSendPokeParams)
	userID, err := t.resolveUser(p.UserID)
	if err != nil {
		return "", err
	}
	// 群会话缺省回退当前群；私聊会话省略群号 → 好友戳一戳
	if strings.TrimSpace(p.GroupID) != "" || t.isGroup {
		var groupID message.QID
		if strings.TrimSpace(p.GroupID) != "" {
			groupID, err = NormalizeID(p.GroupID, t.target)
			if err != nil {
				return "", err
			}
		} else {
			groupID = t.target
		}
		if !t.qq.SendPokeMsg(userID, &groupID) {
			return "", fmt.Errorf("群戳一戳发送失败（可能无权限或协议端不支持）")
		}
		return fmt.Sprintf("已在群 %s 戳了戳 %s", groupID, userID), nil
	}
	if !t.qq.SendPokeMsg(userID, nil) {
		return "", fmt.Errorf("戳一戳发送失败（可能无权限或协议端不支持）")
	}
	return fmt.Sprintf("已戳了戳 %s", userID), nil
}

type qqSendGroupSignParams struct {
	GroupID string `json:"group_id,omitempty" desc:"目标群号；群聊会话可省略（默认当前群），私聊会话必须填写"`
}

type qqSendGroupSignTool struct{ qqBase }

func (t *qqSendGroupSignTool) Name() string { return "qq_send_group_sign" }
func (t *qqSendGroupSignTool) Description() string {
	return "在指定 QQ 群完成今日打卡（签到）；每群每天一次，仅在用户明确要求时使用"
}
func (t *qqSendGroupSignTool) Params() any      { return &qqSendGroupSignParams{} }
func (t *qqSendGroupSignTool) SideEffect() bool { return true }
func (t *qqSendGroupSignTool) Execute(ctx context.Context, params any) (string, error) {
	p := params.(*qqSendGroupSignParams)
	groupID, err := t.resolveGroup(p.GroupID)
	if err != nil {
		return "", err
	}
	if !t.qq.SendGroupSign(groupID) {
		return "", fmt.Errorf("群打卡失败（今日可能已打卡或协议端不支持）")
	}
	return fmt.Sprintf("已在群 %s 完成打卡", groupID), nil
}

// formatUnix Unix 秒转本地时间文本（0 值显示 "-"）。
func formatUnix(ts uint) string {
	if ts == 0 {
		return "-"
	}
	return time.Unix(int64(ts), 0).Format("2006-01-02 15:04:05")
}
