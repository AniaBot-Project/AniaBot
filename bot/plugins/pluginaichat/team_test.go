package pluginaichat

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// ---- 执行参数 ----

func TestTeamMaxMembers(t *testing.T) {
	if teamMaxMembers != 5 {
		t.Fatalf("默认并行成员数 = %d, want 5", teamMaxMembers)
	}
	if teamMaxMembers > teamMaxStoredMembers {
		t.Fatalf("并行成员数 %d 不应超过单团队存储上限 %d", teamMaxMembers, teamMaxStoredMembers)
	}
}

// TestTeamTimeoutFollowsMain 团队成员超时与子代理同源（框架消息处理预算），
// 并对超大 timeout_sec 先限幅再乘 time.Second 防 int64 溢出。
func TestTeamTimeoutFollowsMain(t *testing.T) {
	p := &AIChatPlugin{}
	p.msgEventTimeout = 7 * time.Minute
	got, err := resolveSubagentTimeout(p.subagentTimeout(), 0, context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != 7*time.Minute {
		t.Fatalf("成员超时应跟随框架消息处理预算, got %v", got)
	}

	got, err = resolveSubagentTimeout(p.subagentTimeout(), 10000000000, context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != subagentMaxTimeoutSec*time.Second {
		t.Fatalf("超大 timeout_sec 应被限幅到 %v, got %v", subagentMaxTimeoutSec*time.Second, got)
	}
}

// ---- validateTeamName ----

func TestValidateTeamName(t *testing.T) {
	valid := []string{"开发团队", "team-a", "Team_1", "α小组", "A"}
	for _, name := range valid {
		if err := validateTeamName(name); err != nil {
			t.Fatalf("合法团队名 %q 应通过, got err %v", name, err)
		}
	}
	invalid := []string{"", "  ", "a:b", "a b", strings.Repeat("长", 21)}
	for _, name := range invalid {
		if err := validateTeamName(name); err == nil {
			t.Fatalf("非法团队名 %q 应报错", name)
		}
	}
}

// ---- teamManager ----

func newTestTeamManager(t *testing.T) *teamManager {
	t.Helper()
	return newTeamManager(newPFake(), testLogger())
}

func TestTeamManagerCreateGetList(t *testing.T) {
	m := newTestTeamManager(t)

	// 创建 → 读取
	def, err := m.create("g:123", "开发团队", "前后端开发", []teamMember{
		{Name: "程序员", Role: "写后端"},
		{Name: "审查员", Role: "审代码"},
	})
	if err != nil {
		t.Fatalf("create err = %v", err)
	}
	if def.Name != "开发团队" || len(def.Members) != 2 {
		t.Fatalf("创建结果异常: %+v", def)
	}
	got, ok := m.get("g:123", "开发团队")
	if !ok || got.Desc != "前后端开发" {
		t.Fatalf("get 异常: ok=%v def=%+v", ok, got)
	}

	// 列表按名排序
	if _, err := m.create("g:123", "Alpha", "", []teamMember{{Name: "x"}}); err != nil {
		t.Fatal(err)
	}
	defs := m.list("g:123")
	if len(defs) != 2 || defs[0].Name != "Alpha" || defs[1].Name != "开发团队" {
		t.Fatalf("列表应按名排序, got %+v", defs)
	}

	// scope 隔离：f:123 看不到 g:123 的团队
	if _, ok := m.get("f:123", "开发团队"); ok {
		t.Fatal("跨 scope 不应读到团队")
	}
	if defs := m.list("f:123"); len(defs) != 0 {
		t.Fatalf("跨 scope 列表应为空, got %d", len(defs))
	}
}

func TestTeamManagerDuplicateCreate(t *testing.T) {
	m := newTestTeamManager(t)
	if _, err := m.create("g:1", "团队", "", []teamMember{{Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.create("g:1", "团队", "", []teamMember{{Name: "b"}}); err == nil {
		t.Fatal("重复创建应报错")
	}
}

func TestTeamManagerUpdate(t *testing.T) {
	m := newTestTeamManager(t)
	if _, err := m.create("g:1", "团队", "旧说明", []teamMember{{Name: "a", Role: "旧角色"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.update("g:1", "团队", "新说明", []teamMember{{Name: "b", Role: "新角色"}}); err != nil {
		t.Fatalf("update err = %v", err)
	}
	def, ok := m.get("g:1", "团队")
	if !ok || def.Desc != "新说明" || len(def.Members) != 1 || def.Members[0].Name != "b" {
		t.Fatalf("update 后数据异常: %+v", def)
	}
	if err := m.update("g:1", "不存在", "", []teamMember{{Name: "a"}}); err == nil {
		t.Fatal("更新不存在的团队应报错")
	}
}

func TestTeamManagerDelete(t *testing.T) {
	m := newTestTeamManager(t)
	if _, err := m.create("g:1", "团队", "", []teamMember{{Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	if !m.delete("g:1", "团队") {
		t.Fatal("删除已存在的团队应返回 true")
	}
	if m.delete("g:1", "团队") {
		t.Fatal("删除不存在的团队应返回 false")
	}
	if _, ok := m.get("g:1", "团队"); ok {
		t.Fatal("删除后不应再读到团队")
	}
}

func TestTeamManagerCaps(t *testing.T) {
	m := newTestTeamManager(t)

	// 单 scope 团队数上限（名字用序号区分，避免与 20 字符上限冲突）
	for i := range teamMaxPerScope {
		if _, err := m.create("g:1", fmt.Sprintf("团队%d", i), "", []teamMember{{Name: "a"}}); err != nil {
			t.Fatalf("创建第 %d 个团队失败: %v", i+1, err)
		}
	}
	if _, err := m.create("g:1", "超额团队", "", []teamMember{{Name: "a"}}); err == nil {
		t.Fatal("超过团队数上限应报错")
	}

	// 成员数上限
	members := make([]teamMember, teamMaxStoredMembers+1)
	for i := range members {
		members[i] = teamMember{Name: "m"}
	}
	if _, err := m.create("g:2", "人太多", "", members); err == nil {
		t.Fatal("超过成员数上限应报错")
	}

	// 空成员 / 成员重名
	if _, err := m.create("g:2", "空团队", "", nil); err == nil {
		t.Fatal("空成员应报错")
	}
	if _, err := m.create("g:2", "重名团队", "", []teamMember{{Name: "a"}, {Name: " a "}}); err == nil {
		t.Fatal("成员重名应报错")
	}
}

// ---- 预置角色 ----

func TestLookupTeamRole(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"研究员", true},
		{" 研究员 ", true},
		{"研究员x", false},
		{"", false},
	}
	for _, tt := range tests {
		_, ok := lookupTeamRole(tt.name)
		if ok != tt.want {
			t.Fatalf("lookupTeamRole(%q) = %v, want %v", tt.name, ok, tt.want)
		}
	}
	if names := builtinTeamRoleNames(); !strings.Contains(names, "规划师") || !strings.Contains(names, "编辑") {
		t.Fatalf("角色名列表应包含全部预置角色, got %q", names)
	}
}

// ---- resolveTeamMemberSpec ----

func TestResolveTeamMemberSpec(t *testing.T) {
	m := newTestTeamManager(t)
	if _, err := m.create("g:1", "评审小组", "", []teamMember{{Name: "老张", Role: "负责挑刺"}}); err != nil {
		t.Fatal(err)
	}
	// 全局团队：所有会话的解析链都可回退命中
	if _, err := m.create(teamScopeGlobal, "通用小组", "", []teamMember{{Name: "全球员", Role: "跨会话角色"}}); err != nil {
		t.Fatal(err)
	}
	p := &AIChatPlugin{teamManager: m}

	// ① 内联 role 优先级最高
	spec := p.resolveTeamMemberSpec(m, "g:1", "研究员", "重点核查数据来源")
	if spec.prompt == "" || !strings.Contains(spec.prompt, "重点核查数据来源") {
		t.Fatalf("内联角色应作为提示词, got %q", spec.prompt)
	}
	if spec.degraded != "" {
		t.Fatalf("内联角色不应降级, got %q", spec.degraded)
	}

	// ② 预置角色命中
	spec = p.resolveTeamMemberSpec(m, "g:1", "研究员", "")
	if spec.prompt == "" || !strings.Contains(spec.prompt, "研究员") {
		t.Fatalf("预置角色应使用其提示词, got %q", spec.prompt)
	}
	if spec.degraded != "" {
		t.Fatalf("预置角色不应降级, got %q", spec.degraded)
	}

	// ③ 已存团队成员命中（当前 scope）
	spec = p.resolveTeamMemberSpec(m, "g:1", "老张", "")
	if spec.prompt == "" || !strings.Contains(spec.prompt, "负责挑刺") {
		t.Fatalf("已存团队成员应使用其角色, got %q", spec.prompt)
	}

	// ④ 全局团队成员命中（其它会话也可引用）
	spec = p.resolveTeamMemberSpec(m, "f:9", "全球员", "")
	if spec.prompt == "" || !strings.Contains(spec.prompt, "跨会话角色") {
		t.Fatalf("全局团队成员应可被任意会话引用, got %q", spec.prompt)
	}

	// ⑤ 未知名降级
	spec = p.resolveTeamMemberSpec(m, "g:1", "调研达人", "")
	if spec.prompt != "" || !strings.Contains(spec.degraded, "调研达人") {
		t.Fatalf("未知名应降级, got %+v", spec)
	}
}

// TestResolveTeamMemberSpecRoleTruncate 超长内联角色被截断，防止撑大 system prompt
// （提示词 = 角色名包装 + 截断后的角色描述 + 通用注意段）。
func TestResolveTeamMemberSpecRoleTruncate(t *testing.T) {
	p := &AIChatPlugin{}
	long := strings.Repeat("长", teamRoleMaxRunes*2)
	spec := p.resolveTeamMemberSpec(nil, "g:1", "研究员", long)
	if len([]rune(spec.prompt)) > teamRoleMaxRunes+len([]rune(teamRoleCommonNote))+50 {
		t.Fatalf("角色描述应被截断, 提示词长度 %d", len([]rune(spec.prompt)))
	}
	if strings.Contains(spec.prompt, strings.Repeat("长", teamRoleMaxRunes+100)) {
		t.Fatal("超长角色描述未截断")
	}
}

// ---- buildTeamReport ----

func TestBuildTeamReport(t *testing.T) {
	base := func(label, result string) teamMemberResult {
		return teamMemberResult{label: label, result: result}
	}
	err := func(label string, e error) teamMemberResult {
		return teamMemberResult{label: label, err: e}
	}

	t.Run("全成功", func(t *testing.T) {
		report := buildTeamReport([]teamMemberResult{
			base("研究员", "【子代理执行完成】耗时 1.0s · LLM 轮数 2 · token 100\n结论A"),
			base("编辑", "【子代理执行完成】耗时 2.0s · LLM 轮数 3 · token 200\n结论B"),
		}, 3*time.Second)
		for _, want := range []string{"成员 2 人（成功 2 · 失败 0）", "===== 成员 1/2：研究员 =====", "===== 成员 2/2：编辑 =====", "结论A", "结论B"} {
			if !strings.Contains(report, want) {
				t.Fatalf("报告应包含 %q, got:\n%s", want, report)
			}
		}
	})

	t.Run("混合成功失败与降级", func(t *testing.T) {
		report := buildTeamReport([]teamMemberResult{
			{label: "调研达人", result: "【子代理执行完成】耗时 1.0s · LLM 轮数 1 · token 10\n结果", degraded: "未识别的角色「调研达人」，已按普通子代理执行"},
			err("程序员", context.DeadlineExceeded),
			base("编辑", ""),
		}, 5*time.Second)
		// 空结果算成功（err 为 nil），故成功 2 失败 1
		for _, want := range []string{"成员 3 人（成功 2 · 失败 1）", "（未识别的角色「调研达人」", "执行失败", "（成员没有返回内容）"} {
			if !strings.Contains(report, want) {
				t.Fatalf("报告应包含 %q, got:\n%s", want, report)
			}
		}
	})

	t.Run("输入顺序保持", func(t *testing.T) {
		report := buildTeamReport([]teamMemberResult{
			base("成员C", "结果C"),
			base("成员A", "结果A"),
			base("成员B", "结果B"),
		}, time.Second)
		idxC, idxA, idxB := strings.Index(report, "成员C"), strings.Index(report, "成员A"), strings.Index(report, "成员B")
		if !(idxC < idxA && idxA < idxB) {
			t.Fatalf("报告应按输入顺序输出, got:\n%s", report)
		}
	})
}

// ---- team_run 参数校验 ----

func newTeamToolPlugin(t *testing.T) *AIChatPlugin {
	t.Helper()
	p := &AIChatPlugin{}
	p.teamManager = newTeamManager(newPFake(), testLogger())
	return p
}

// TestTeamEffectiveTask leader 分工的任务组合：未填专属任务回退总体任务，
// 填了专属任务时拼接在总体任务之后。
func TestTeamEffectiveTask(t *testing.T) {
	base := "评估项目的代码质量与可测试性"

	// 成员未填专属任务：回退为总体任务
	if got := teamEffectiveTask(base, ""); got != base {
		t.Fatalf("未填专属任务应回退总体任务, got %q", got)
	}
	// 成员专属任务为纯空白：视为未填，回退总体任务
	if got := teamEffectiveTask(base, "   "); got != base {
		t.Fatalf("空白专属任务应回退总体任务, got %q", got)
	}
	// 成员填了专属任务：总体任务（背景）+ 专属任务拼接
	own := "审查 bot/core 目录下的后端代码，重点看并发与错误处理"
	got := teamEffectiveTask(base, own)
	if !strings.HasPrefix(got, base) || !strings.HasSuffix(got, own) {
		t.Fatalf("专属任务应拼接在总体任务之后, got %q", got)
	}
	if !strings.Contains(got, "\n\n") {
		t.Fatalf("总体任务与专属任务之间应有空行分隔, got %q", got)
	}
}

func TestTeamRunToolValidation(t *testing.T) {
	p := newTeamToolPlugin(t)
	tool := newTeamTools(p, nil, message.FromUint64(123), true)[0]

	if _, err := tool.Execute(context.Background(), &teamRunParams{Task: "   ", Members: []teamMemberParam{{Name: "研究员"}}}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("空 task 应报错")
	}
	if _, err := tool.Execute(context.Background(), &teamRunParams{Task: "任务", Members: nil}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("空 members 应报错")
	}
	tooMany := make([]teamMemberParam, teamMaxMembers+1)
	for i := range tooMany {
		tooMany[i] = teamMemberParam{Name: "研究员"}
	}
	if _, err := tool.Execute(context.Background(), &teamRunParams{Task: "任务", Members: tooMany}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("超上限成员数应报错")
	}
	if _, err := tool.Execute(context.Background(), &teamRunParams{Task: "任务", Members: []teamMemberParam{{Name: "  "}}}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("成员 name 为空应报错")
	}
	if _, err := tool.Execute(context.Background(), &teamRunParams{Task: "任务", Members: []teamMemberParam{{Name: "研究员"}, {Name: " 研究员 "}}}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("成员名重复应报错")
	}
}

// ---- newTeamTools ----

func TestNewTeamTools(t *testing.T) {
	p := newTeamToolPlugin(t)

	groupTools := newTeamTools(p, nil, message.FromUint64(12345), true)
	if len(groupTools) != 4 {
		t.Fatalf("应有 4 个团队工具（run/create/list/delete）, got %d", len(groupTools))
	}
	if groupTools[0].Name() != "team_run" {
		t.Fatalf("工具名 = %q, want team_run", groupTools[0].Name())
	}
	desc := groupTools[0].Description()
	for _, want := range []string{"群聊（会话 ID qq:12345）", "规划师", "研究员", "编辑", "300 秒", "5 个成员"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("team_run 描述应包含 %q, got %q", want, desc)
		}
	}

	friendTools := newTeamTools(p, nil, message.FromUint64(67890), false)
	if !strings.Contains(friendTools[0].Description(), "私聊（对方 ID qq:67890）") {
		t.Fatalf("私聊会话描述应包含对方 ID, got %q", friendTools[0].Description())
	}
}

// ---- Web 面板数据源（teampanel.go） ----

func TestTeamPanelSource(t *testing.T) {
	p := &AIChatPlugin{teamManager: newTestTeamManager(t)}
	p.Logger = testLogger()

	// 未启用时返回统一错误
	p.teamManager = nil
	if _, err := p.TeamList("g:1"); err == nil {
		t.Fatal("功能未启用时应报错")
	}
	p.teamManager = newTestTeamManager(t)

	// 非法 scope 一律拒绝
	if err := p.TeamCreate(plugininfo.TeamUpsert{Scope: "hack:1", Name: "x", Members: []plugininfo.TeamMemberInfo{{Name: "a"}}}); err == nil {
		t.Fatal("非法 scope 应报错")
	}

	// 创建 → 列表
	err := p.TeamCreate(plugininfo.TeamUpsert{
		Scope: "g:1",
		Name:  "开发团队",
		Desc:  "前后端",
		Members: []plugininfo.TeamMemberInfo{
			{Name: "程序员", Role: "写代码"},
			{Name: "审查员"},
		},
	})
	if err != nil {
		t.Fatalf("TeamCreate err = %v", err)
	}
	infos, err := p.TeamList("g:1")
	if err != nil || len(infos) != 1 {
		t.Fatalf("TeamList 异常: %v len %d", err, len(infos))
	}
	info := infos[0]
	if info.Name != "开发团队" || len(info.Members) != 2 || info.Members[0].Role != "写代码" {
		t.Fatalf("列表数据异常: %+v", info)
	}

	// 更新 → 读取
	err = p.TeamUpdate(plugininfo.TeamUpsert{
		Scope:   "g:1",
		Name:    "开发团队",
		Desc:    "仅后端",
		Members: []plugininfo.TeamMemberInfo{{Name: "后端工程师", Role: "写后端"}},
	})
	if err != nil {
		t.Fatalf("TeamUpdate err = %v", err)
	}
	infos, _ = p.TeamList("g:1")
	if infos[0].Desc != "仅后端" || len(infos[0].Members) != 1 || infos[0].Members[0].Name != "后端工程师" {
		t.Fatalf("更新后数据异常: %+v", infos[0])
	}

	// 全局团队：任意 scope 合法，scopes 汇总带 global 种类
	if err := p.TeamCreate(plugininfo.TeamUpsert{
		Scope:   "global",
		Name:    "通用小组",
		Members: []plugininfo.TeamMemberInfo{{Name: "全球员", Role: "跨会话角色"}},
	}); err != nil {
		t.Fatalf("global scope 创建失败: %v", err)
	}

	// scopes 汇总
	_ = p.TeamCreate(plugininfo.TeamUpsert{Scope: "f:2", Name: "私聊团队", Members: []plugininfo.TeamMemberInfo{{Name: "a"}}})
	scopes := p.TeamScopes()
	if len(scopes) != 3 {
		t.Fatalf("TeamScopes 数量异常: %+v", scopes)
	}
	byScope := map[string]plugininfo.TeamScopeInfo{}
	for _, s := range scopes {
		byScope[s.Scope] = s
	}
	if s := byScope["global"]; s.Kind != "global" || s.Count != 1 {
		t.Fatalf("global scope 信息异常: %+v", s)
	}
	if s := byScope["f:2"]; s.Kind != "friend" || s.Count != 1 {
		t.Fatalf("f:2 scope 信息异常: %+v", s)
	}

	// 预置角色列表（供面板选择器）
	roles := p.TeamRoles()
	if len(roles) != len(builtinTeamRoles) || roles[0].Name != builtinTeamRoles[0].Name || roles[0].Summary == "" {
		t.Fatalf("TeamRoles 异常: %+v", roles)
	}

	// 删除 → 不存在报错
	if err := p.TeamDelete("g:1", "开发团队"); err != nil {
		t.Fatalf("TeamDelete err = %v", err)
	}
	if err := p.TeamDelete("g:1", "开发团队"); err == nil {
		t.Fatal("删除不存在的团队应报错")
	}
}

// ---- team_create / team_list / team_delete 端到端 ----

func TestTeamManageTools(t *testing.T) {
	p := newTeamToolPlugin(t)
	tools := newTeamTools(p, nil, message.FromUint64(1), true)
	var create, list, del llmtool.Tool
	for _, tool := range tools {
		switch tool.Name() {
		case "team_create":
			create = tool
		case "team_list":
			list = tool
		case "team_delete":
			del = tool
		}
	}
	if create == nil || list == nil || del == nil {
		t.Fatal("四个工具不齐")
	}

	res, err := create.Execute(context.Background(), &teamCreateParams{
		Name:    "开发团队",
		Desc:    "前后端开发",
		Members: []teamMemberParam{{Name: "程序员", Role: "写代码"}, {Name: "审查员", Role: "审代码"}},
	}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(res, "已创建") {
		t.Fatalf("create 结果异常: %q err %v", res, err)
	}

	res, err = list.Execute(context.Background(), &teamListParams{}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(res, "开发团队") || !strings.Contains(res, "程序员 / 审查员") {
		t.Fatalf("list 结果异常: %q err %v", res, err)
	}

	res, err = del.Execute(context.Background(), &teamDeleteParams{Name: "开发团队"}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(res, "已删除") {
		t.Fatalf("delete 结果异常: %q err %v", res, err)
	}
	res, err = list.Execute(context.Background(), &teamListParams{}, llmtool.CallBackFuncs{})
	if err != nil || !strings.Contains(res, "还没有保存的团队") {
		t.Fatalf("删除后 list 应为空: %q err %v", res, err)
	}
}
