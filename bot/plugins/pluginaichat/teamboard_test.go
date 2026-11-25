package pluginaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/jeanhua/AniaBot/common/plugin"
)

// ---- 测试辅助 ----

// newTeamBoardTestPlugin 构造带后台任务表（内存 SQLite）的测试插件。
// 注意：嵌入字段不能在复合字面量里直接赋值，Logger 需构造后赋值。
func newTeamBoardTestPlugin(t *testing.T) *AIChatPlugin {
	t.Helper()
	store, _ := newTaskTestStore(t)
	p := &AIChatPlugin{Meta: plugin.Meta{Name: "AI对话插件"}, taskStore: store}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return p
}

// teamBoardTestTools 构造绑定到群聊会话 1001 的 4 个任务板工具，
// 按契约顺序返回 create/list/claim/complete。
func teamBoardTestTools(t *testing.T, p *AIChatPlugin) (create, list, claim, complete llmtool.Tool) {
	t.Helper()
	tools := newTeamBoardTools(p, message.FromUint64(1001), true)
	if len(tools) != 4 {
		t.Fatalf("团队任务板工具数 = %d, want 4", len(tools))
	}
	return tools[0], tools[1], tools[2], tools[3]
}

// execTeamBoard 执行一个任务板工具（无回调依赖）。
func execTeamBoard(t *testing.T, tool llmtool.Tool, params any) (string, error) {
	t.Helper()
	return tool.Execute(context.Background(), params, llmtool.CallBackFuncs{})
}

// mustCreateTeamTask 创建任务并返回从工具输出中解析出的任务 ID。
func mustCreateTeamTask(t *testing.T, create llmtool.Tool, params *teamTaskCreateParams) string {
	t.Helper()
	out, err := execTeamBoard(t, create, params)
	if err != nil {
		t.Fatalf("创建团队任务 %q 失败: %v", params.Title, err)
	}
	return teamBoardCreatedID(t, out)
}

// teamBoardCreatedID 从「已创建团队任务 <ID>（标题）」输出中解析任务 ID。
func teamBoardCreatedID(t *testing.T, out string) string {
	t.Helper()
	const prefix = "已创建团队任务 "
	i := strings.Index(out, prefix)
	if i < 0 {
		t.Fatalf("创建输出缺少「已创建团队任务」: %q", out)
	}
	rest := out[i+len(prefix):]
	if j := strings.IndexAny(rest, "（(\n "); j >= 0 {
		rest = rest[:j]
	}
	id := strings.TrimSpace(rest)
	if !strings.HasPrefix(id, "t-") {
		t.Fatalf("创建输出解析任务 ID 失败: %q（输出 %q）", id, out)
	}
	return id
}

// mustLoadTeamTask 读回任务记录与解析后的 Meta。
func mustLoadTeamTask(t *testing.T, p *AIChatPlugin, id string) (*agentTask, teamTaskMeta) {
	t.Helper()
	ctx, cancel := p.taskDB()
	defer cancel()
	task, ok, err := p.taskStore.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("读取团队任务 %s: ok=%v err=%v", id, ok, err)
	}
	return task, parseTeamTaskMeta(task.Meta)
}

// ---- 工具集 ----

// TestTeamBoardToolSet 工具名与顺序必须与契约一致：4 个，create/list/claim/complete。
func TestTeamBoardToolSet(t *testing.T) {
	p := newTeamBoardTestPlugin(t)
	tools := newTeamBoardTools(p, message.FromUint64(1001), true)
	if len(tools) != 4 {
		t.Fatalf("团队任务板工具数 = %d, want 4", len(tools))
	}
	want := []string{"team_task_create", "team_task_list", "team_task_claim", "team_task_complete"}
	for i, name := range want {
		if got := tools[i].Name(); got != name {
			t.Fatalf("第 %d 个工具名 = %q, want %q", i, got, name)
		}
		if tools[i].Description() == "" {
			t.Fatalf("工具 %s 描述为空", name)
		}
		if tools[i].Params() == nil {
			t.Fatalf("工具 %s 参数结构为 nil", name)
		}
	}
	// 私聊会话绑定同样可用
	friendTools := newTeamBoardTools(p, message.FromUint64(1), false)
	if len(friendTools) != 4 || friendTools[0].Name() != "team_task_create" {
		t.Fatalf("私聊工具集异常: %d 个", len(friendTools))
	}
}

// TestTeamBoardDisabledWithoutStore taskStore 未启用时 4 个工具都返回可读提示，
// 不返回 error。
func TestTeamBoardDisabledWithoutStore(t *testing.T) {
	p := &AIChatPlugin{Meta: plugin.Meta{Name: "AI对话插件"}}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	create, list, claim, complete := teamBoardTestTools(t, p)
	cases := []struct {
		name   string
		tool   llmtool.Tool
		params any
	}{
		{"create", create, &teamTaskCreateParams{Title: "任务"}},
		{"list", list, &teamTaskListParams{}},
		{"claim", claim, &teamTaskClaimParams{ID: "t-abc", Owner: "alice"}},
		{"complete", complete, &teamTaskCompleteParams{ID: "t-abc"}},
	}
	for _, c := range cases {
		out, err := execTeamBoard(t, c.tool, c.params)
		if err != nil {
			t.Fatalf("%s 在无存储时应返回文本而非 error: %v", c.name, err)
		}
		if !strings.Contains(out, "任务板未启用") {
			t.Fatalf("%s 提示不符: %q", c.name, out)
		}
	}
}

// ---- create ----

// TestTeamBoardCreateValidation 覆盖 create 的校验与落库：
// 空标题、依赖不存在、跨会话依赖、非团队任务依赖、自依赖，以及合法创建后
// kind/status/session/Title/Prompt/Meta(去重后的 write_scopes) 的落库结果。
func TestTeamBoardCreateValidation(t *testing.T) {
	p := newTeamBoardTestPlugin(t)
	create, _, _, _ := teamBoardTestTools(t, p)
	key := sessionKey(message.FromUint64(1001), true)
	dbCtx, cancel := p.taskDB()
	defer cancel()

	// 空标题
	if _, err := execTeamBoard(t, create, &teamTaskCreateParams{Title: "   "}); err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("空标题应返回参数错误, got %v", err)
	}

	// 依赖不存在
	if _, err := execTeamBoard(t, create, &teamTaskCreateParams{Title: "A", BlockedBy: []string{"t-missing"}}); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("依赖不存在应报错, got %v", err)
	}

	// 跨会话依赖：别的会话的团队任务不能当依赖
	foreign := &agentTask{SessionKey: "f:999", Kind: taskKindTeamTask, Status: taskStatusQueued, Title: "别的会话任务"}
	if err := p.taskStore.Create(dbCtx, foreign); err != nil {
		t.Fatalf("Create foreign: %v", err)
	}
	if _, err := execTeamBoard(t, create, &teamTaskCreateParams{Title: "A", BlockedBy: []string{foreign.ID}}); err == nil || !strings.Contains(err.Error(), "不属于当前会话") {
		t.Fatalf("跨会话依赖应报错, got %v", err)
	}

	// 非团队任务不能当依赖
	other := &agentTask{SessionKey: key, Kind: taskKindTurn, Status: taskStatusRunning, Title: "普通回合"}
	if err := p.taskStore.Create(dbCtx, other); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	if _, err := execTeamBoard(t, create, &teamTaskCreateParams{Title: "A", BlockedBy: []string{other.ID}}); err == nil || !strings.Contains(err.Error(), "不是团队任务") {
		t.Fatalf("非团队任务依赖应报错, got %v", err)
	}

	// 自依赖（契约要求的显式校验，直接验证校验函数）
	if err := p.validateTeamTaskBlockers(dbCtx, key, []string{foreign.ID}, foreign.ID); err == nil || !strings.Contains(err.Error(), "依赖自身") {
		t.Fatalf("自依赖应报错, got %v", err)
	}

	// 合法创建：WriteScopes 去空白/去重，Title/Prompt 落库
	baseID := mustCreateTeamTask(t, create, &teamTaskCreateParams{
		Title:       "实现登录接口",
		Description: "在 bot/api 下实现 /login 并补单测",
		WriteScopes: []string{" bot/api/login.go ", "bot/api/login.go", "  "},
	})
	base, baseMeta := mustLoadTeamTask(t, p, baseID)
	if base.Kind != taskKindTeamTask || base.Status != taskStatusQueued || base.SessionKey != key {
		t.Fatalf("新建任务字段不符: %+v", base)
	}
	if base.Title != "实现登录接口" || base.Prompt != "在 bot/api 下实现 /login 并补单测" {
		t.Fatalf("Title/Prompt 落库不符: %+v", base)
	}
	if len(baseMeta.WriteScopes) != 1 || baseMeta.WriteScopes[0] != "bot/api/login.go" {
		t.Fatalf("write_scopes 未去空白/去重: %+v", baseMeta.WriteScopes)
	}

	// 依赖合法：新任务依赖已有团队任务，Meta 可反序列化
	depID := mustCreateTeamTask(t, create, &teamTaskCreateParams{
		Title:     "调用登录接口",
		BlockedBy: []string{baseID},
	})
	depTask, depMeta := mustLoadTeamTask(t, p, depID)
	if len(depMeta.BlockedBy) != 1 || depMeta.BlockedBy[0] != baseID {
		t.Fatalf("blocked_by 落库不符: %+v", depMeta.BlockedBy)
	}
	var raw teamTaskMeta
	if err := json.Unmarshal([]byte(depTask.Meta), &raw); err != nil {
		t.Fatalf("Meta 不是合法 JSON: %q (%v)", depTask.Meta, err)
	}
	if raw.BlockedBy[0] != baseID {
		t.Fatalf("Meta JSON blocked_by 不符: %+v", raw.BlockedBy)
	}
}

// TestTeamBoardCreateLimit 单会话未完成任务上限 20：第 21 个报错；完成一个
// 腾出名额后可以继续创建（已完成任务是历史，不占上限）。
func TestTeamBoardCreateLimit(t *testing.T) {
	p := newTeamBoardTestPlugin(t)
	create, _, claim, complete := teamBoardTestTools(t, p)

	ids := make([]string, 0, teamBoardMaxTasks)
	for i := 0; i < teamBoardMaxTasks; i++ {
		ids = append(ids, mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: fmt.Sprintf("任务 %d", i+1)}))
	}
	if _, err := execTeamBoard(t, create, &teamTaskCreateParams{Title: "超额任务"}); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超过 %d 个任务应报错, got %v", teamBoardMaxTasks, err)
	}

	// 完成一个任务后未完成数量回落，可继续创建
	if out, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: ids[0], Owner: "alice"}); err != nil || !strings.Contains(out, "认领成功") {
		t.Fatalf("claim: out=%q err=%v", out, err)
	}
	if out, err := execTeamBoard(t, complete, &teamTaskCompleteParams{ID: ids[0], Owner: "alice", Result: "完成"}); err != nil || !strings.Contains(out, "已完成") {
		t.Fatalf("complete: out=%q err=%v", out, err)
	}
	if id := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "补位任务"}); id == "" {
		t.Fatal("完成旧任务后应能继续创建")
	}
}

// ---- list ----

// TestTeamBoardList 列表过滤与渲染：空任务板返回"任务板为空"；默认过滤
// 已完成任务；include_completed=true 时包含；行格式为
// 「ID | 状态 | 负责人 | 标题 | 阻塞依赖」；非团队任务不出现。
func TestTeamBoardList(t *testing.T) {
	p := newTeamBoardTestPlugin(t)
	create, list, claim, complete := teamBoardTestTools(t, p)

	if out, err := execTeamBoard(t, list, &teamTaskListParams{}); err != nil || out != "任务板为空" {
		t.Fatalf("空任务板 list = (%q, %v), want (任务板为空, nil)", out, err)
	}

	doneID := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "已完成任务"})
	todoID := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "待办任务", WriteScopes: []string{"bot/todo.go"}})
	if out, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: doneID, Owner: "alice"}); err != nil || !strings.Contains(out, "认领成功") {
		t.Fatalf("claim: out=%q err=%v", out, err)
	}
	if out, err := execTeamBoard(t, complete, &teamTaskCompleteParams{ID: doneID, Owner: "alice", Result: "done"}); err != nil || !strings.Contains(out, "已完成") {
		t.Fatalf("complete: out=%q err=%v", out, err)
	}

	out, err := execTeamBoard(t, list, &teamTaskListParams{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "ID | 状态 | 负责人 | 标题 | 阻塞依赖") {
		t.Fatalf("列表缺少表头: %q", out)
	}
	if strings.Contains(out, doneID) {
		t.Fatalf("默认列表不应包含已完成任务: %q", out)
	}
	if !strings.Contains(out, todoID) || !strings.Contains(out, "待办任务") || !strings.Contains(out, "| 排队 |") {
		t.Fatalf("列表缺少未完成任务行: %q", out)
	}
	if !strings.Contains(out, "写作用域：bot/todo.go") {
		t.Fatalf("列表未展示写作用域: %q", out)
	}

	out, err = execTeamBoard(t, list, &teamTaskListParams{IncludeCompleted: true})
	if err != nil {
		t.Fatalf("list include_completed: %v", err)
	}
	if !strings.Contains(out, doneID) || !strings.Contains(out, "| 完成 |") || !strings.Contains(out, "alice") {
		t.Fatalf("include_completed 列表缺少已完成任务/负责人: %q", out)
	}

	// 非团队任务不进入任务板
	ctx, cancel := p.taskDB()
	defer cancel()
	if err := p.taskStore.Create(ctx, &agentTask{
		SessionKey: sessionKey(message.FromUint64(1001), true),
		Kind:       taskKindSubagent,
		Status:     taskStatusRunning,
		Title:      "子代理任务不应出现",
	}); err != nil {
		t.Fatalf("Create subagent: %v", err)
	}
	out, err = execTeamBoard(t, list, &teamTaskListParams{})
	if err != nil {
		t.Fatalf("list after subagent: %v", err)
	}
	if strings.Contains(out, "子代理任务不应出现") {
		t.Fatalf("非 team_task 行不应出现在任务板: %q", out)
	}
}

// ---- claim ----

// TestTeamBoardClaimPersistsOwner 认领成功后 status=running、Meta.Owner 落库，
// 重复认领被拒绝且不改变负责人。
func TestTeamBoardClaimPersistsOwner(t *testing.T) {
	p := newTeamBoardTestPlugin(t)
	create, _, claim, _ := teamBoardTestTools(t, p)
	id := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "实现接口", Description: "补全 handler", WriteScopes: []string{"bot/handler.go"}})

	out, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: id, Owner: "alice"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !strings.Contains(out, "认领成功") || !strings.Contains(out, "alice") {
		t.Fatalf("认领输出不符: %q", out)
	}
	if !strings.Contains(out, "补全 handler") {
		t.Fatalf("认领输出应含任务描述: %q", out)
	}

	task, meta := mustLoadTeamTask(t, p, id)
	if task.Status != taskStatusRunning {
		t.Fatalf("认领后状态 = %q, want %q", task.Status, taskStatusRunning)
	}
	if task.StartedAt == 0 {
		t.Fatal("认领后 started_at 未写入")
	}
	if meta.Owner != "alice" {
		t.Fatalf("Meta.Owner = %q, want alice", meta.Owner)
	}
	if !strings.Contains(task.Meta, `"owner":"alice"`) {
		t.Fatalf("Meta JSON 未包含 owner: %q", task.Meta)
	}

	// 重复认领：期望状态类失败，返回文本而非 error
	out, err = execTeamBoard(t, claim, &teamTaskClaimParams{ID: id, Owner: "bob"})
	if err != nil {
		t.Fatalf("重复认领应返回文本而非 error: %v", err)
	}
	if !strings.Contains(out, "已被") || !strings.Contains(out, "alice") {
		t.Fatalf("重复认领原因不符: %q", out)
	}
	task, meta = mustLoadTeamTask(t, p, id)
	if meta.Owner != "alice" || task.Status != taskStatusRunning {
		t.Fatalf("重复认领不应改变任务: status=%q owner=%q", task.Status, meta.Owner)
	}
}

// TestTeamBoardClaimByUniquePrefix 支持会话内唯一 ID 前缀；歧义前缀返回提示。
func TestTeamBoardClaimByUniquePrefix(t *testing.T) {
	p := newTeamBoardTestPlugin(t)
	create, _, claim, _ := teamBoardTestTools(t, p)
	id := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "前缀任务"})
	prefix := id[:len(id)-1]
	out, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: prefix, Owner: "alice"})
	if err != nil || !strings.Contains(out, "认领成功") {
		t.Fatalf("唯一前缀认领失败: out=%q err=%v", out, err)
	}

	if _, err := execTeamBoard(t, create, &teamTaskCreateParams{Title: "第二个任务"}); err != nil {
		t.Fatalf("create second: %v", err)
	}
	out, err = execTeamBoard(t, claim, &teamTaskClaimParams{ID: "t-", Owner: "bob"})
	if err != nil {
		t.Fatalf("歧义前缀应返回文本而非 error: %v", err)
	}
	if !strings.Contains(out, "匹配到多个") {
		t.Fatalf("歧义前缀提示不符: %q", out)
	}
}

// TestTeamBoardClaimRejectsForeignAndNonTeamTask 跨会话任务、非 team_task 任务、
// 不存在的 ID 都拒绝认领；参数非法返回 error。
func TestTeamBoardClaimRejectsForeignAndNonTeamTask(t *testing.T) {
	p := newTeamBoardTestPlugin(t)
	_, _, claim, complete := teamBoardTestTools(t, p)
	key := sessionKey(message.FromUint64(1001), true)
	ctx, cancel := p.taskDB()
	defer cancel()

	foreign := &agentTask{SessionKey: "f:999", Kind: taskKindTeamTask, Status: taskStatusQueued, Title: "别的会话任务"}
	if err := p.taskStore.Create(ctx, foreign); err != nil {
		t.Fatalf("Create foreign: %v", err)
	}
	out, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: foreign.ID, Owner: "alice"})
	if err != nil || !strings.Contains(out, "只能认领本会话") {
		t.Fatalf("跨会话认领应拒绝: out=%q err=%v", out, err)
	}
	out, err = execTeamBoard(t, complete, &teamTaskCompleteParams{ID: foreign.ID})
	if err != nil || !strings.Contains(out, "只能完成本会话") {
		t.Fatalf("跨会话完成应拒绝: out=%q err=%v", out, err)
	}

	other := &agentTask{SessionKey: key, Kind: taskKindSubagent, Status: taskStatusQueued, Title: "子代理任务"}
	if err := p.taskStore.Create(ctx, other); err != nil {
		t.Fatalf("Create other: %v", err)
	}
	out, err = execTeamBoard(t, claim, &teamTaskClaimParams{ID: other.ID, Owner: "alice"})
	if err != nil || !strings.Contains(out, "不是团队任务") {
		t.Fatalf("非团队任务认领应拒绝: out=%q err=%v", out, err)
	}

	out, err = execTeamBoard(t, claim, &teamTaskClaimParams{ID: "t-not-exist", Owner: "alice"})
	if err != nil || !strings.Contains(out, "未找到") {
		t.Fatalf("不存在任务认领提示不符: out=%q err=%v", out, err)
	}

	// 参数非法：返回 error
	if _, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: "  ", Owner: "alice"}); err == nil {
		t.Fatal("空 id 应返回 error")
	}
	if _, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: "t-abc", Owner: "  "}); err == nil {
		t.Fatal("空 owner 应返回 error")
	}
}

// ---- complete 与依赖解除 ----

// TestTeamBoardDependencyFlow 依赖未完成时不能认领；完成依赖后 complete 输出
// 列出因此解除阻塞的任务，且该任务变为可认领。
func TestTeamBoardDependencyFlow(t *testing.T) {
	p := newTeamBoardTestPlugin(t)
	create, list, claim, complete := teamBoardTestTools(t, p)

	firstID := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "第一步"})
	secondID := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "第二步", BlockedBy: []string{firstID}})

	// 列表里能看到依赖关系
	out, err := execTeamBoard(t, list, &teamTaskListParams{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, secondID) || !strings.Contains(out, firstID) {
		t.Fatalf("列表缺少任务或依赖: %q", out)
	}

	// 依赖未完成：认领被拒绝，状态保持 queued
	out, err = execTeamBoard(t, claim, &teamTaskClaimParams{ID: secondID, Owner: "bob"})
	if err != nil {
		t.Fatalf("依赖未完成认领应返回文本而非 error: %v", err)
	}
	if !strings.Contains(out, "依赖尚未全部完成") || !strings.Contains(out, firstID) {
		t.Fatalf("依赖未完成原因不符: %q", out)
	}
	if task, _ := mustLoadTeamTask(t, p, secondID); task.Status != taskStatusQueued {
		t.Fatalf("依赖未完成时状态不应变化: %q", task.Status)
	}

	// 认领并完成第一步
	if out, err = execTeamBoard(t, claim, &teamTaskClaimParams{ID: firstID, Owner: "alice"}); err != nil || !strings.Contains(out, "认领成功") {
		t.Fatalf("claim first: out=%q err=%v", out, err)
	}
	out, err = execTeamBoard(t, complete, &teamTaskCompleteParams{ID: firstID, Owner: "alice", Result: "第一步完成"})
	if err != nil {
		t.Fatalf("complete first: %v", err)
	}
	if !strings.Contains(out, "已完成") || !strings.Contains(out, secondID) {
		t.Fatalf("完成输出应列出解除阻塞的任务 %s: %q", secondID, out)
	}

	// 现在第二步可以认领
	out, err = execTeamBoard(t, claim, &teamTaskClaimParams{ID: secondID, Owner: "bob"})
	if err != nil || !strings.Contains(out, "认领成功") {
		t.Fatalf("依赖完成后应可认领: out=%q err=%v", out, err)
	}
	// 完成第二步：没有后续任务
	out, err = execTeamBoard(t, complete, &teamTaskCompleteParams{ID: secondID, Owner: "bob"})
	if err != nil || !strings.Contains(out, "没有其他任务因此解除阻塞") {
		t.Fatalf("末端任务完成提示不符: out=%q err=%v", out, err)
	}
}

// TestTeamBoardCompleteRecordsResult complete 写入 status/FinishedAt/Meta.Result，
// 并校验 owner；已完成任务不能再完成、他人不能代完成。
func TestTeamBoardCompleteRecordsResult(t *testing.T) {
	p := newTeamBoardTestPlugin(t)
	create, _, claim, complete := teamBoardTestTools(t, p)

	id := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "写文档"})
	if out, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: id, Owner: "alice"}); err != nil || !strings.Contains(out, "认领成功") {
		t.Fatalf("claim: out=%q err=%v", out, err)
	}

	const result = "文档已写入 docs/teamboard.md，验收通过"
	out, err := execTeamBoard(t, complete, &teamTaskCompleteParams{ID: id, Owner: "alice", Result: result})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !strings.Contains(out, "已完成") || !strings.Contains(out, id) {
		t.Fatalf("完成输出不符: %q", out)
	}
	task, meta := mustLoadTeamTask(t, p, id)
	if task.Status != taskStatusCompleted || task.FinishedAt == 0 || task.StopReason != taskStopCompleted {
		t.Fatalf("完成状态落库不符: %+v", task)
	}
	if meta.Owner != "alice" || meta.Result != result {
		t.Fatalf("Meta 完成信息不符: %+v", meta)
	}
	var raw teamTaskMeta
	if err := json.Unmarshal([]byte(task.Meta), &raw); err != nil || raw.Result != result {
		t.Fatalf("Meta.Result 未正确入库: json=%q err=%v", task.Meta, err)
	}

	// 已完成任务再次完成：期望状态类失败
	out, err = execTeamBoard(t, complete, &teamTaskCompleteParams{ID: id})
	if err != nil || !strings.Contains(out, "只有已认领") {
		t.Fatalf("重复完成提示不符: out=%q err=%v", out, err)
	}

	// owner 不匹配：拒绝代完成，状态不变
	id2 := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "另一任务"})
	if out, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: id2, Owner: "alice"}); err != nil || !strings.Contains(out, "认领成功") {
		t.Fatalf("claim id2: out=%q err=%v", out, err)
	}
	out, err = execTeamBoard(t, complete, &teamTaskCompleteParams{ID: id2, Owner: "bob", Result: "x"})
	if err != nil || !strings.Contains(out, "owner 不匹配") {
		t.Fatalf("owner 不匹配提示不符: out=%q err=%v", out, err)
	}
	if task2, meta2 := mustLoadTeamTask(t, p, id2); task2.Status != taskStatusRunning || meta2.Result != "" {
		t.Fatalf("owner 不匹配不应完成任务: status=%q result=%q", task2.Status, meta2.Result)
	}

	// 超长结果按 4000 rune 截断后入库
	id3 := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "超长结果任务"})
	if out, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: id3, Owner: "carol"}); err != nil || !strings.Contains(out, "认领成功") {
		t.Fatalf("claim id3: out=%q err=%v", out, err)
	}
	longResult := strings.Repeat("长", teamTaskResultMaxRunes+100)
	if _, err := execTeamBoard(t, complete, &teamTaskCompleteParams{ID: id3, Owner: "carol", Result: longResult}); err != nil {
		t.Fatalf("complete long: %v", err)
	}
	_, meta3 := mustLoadTeamTask(t, p, id3)
	if got := len([]rune(meta3.Result)); got <= teamTaskResultMaxRunes || got > teamTaskResultMaxRunes+len([]rune("\n…（结果已截断）")) {
		t.Fatalf("超长结果截断长度不符: %d", got)
	}
	if !strings.Contains(meta3.Result, "结果已截断") {
		t.Fatalf("超长结果缺少截断提示: %q", meta3.Result)
	}
}

// TestTeamBoardMetaParseFallback Meta 损坏时按空结构处理，不阻断认领。
func TestTeamBoardMetaParseFallback(t *testing.T) {
	p := newTeamBoardTestPlugin(t)
	create, _, claim, _ := teamBoardTestTools(t, p)
	id := mustCreateTeamTask(t, create, &teamTaskCreateParams{Title: "坏 Meta"})

	ctx, cancel := p.taskDB()
	task, ok, err := p.taskStore.Get(ctx, id)
	if err != nil || !ok {
		cancel()
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	task.Meta = "{not-json"
	if err := p.taskStore.Update(ctx, task); err != nil {
		cancel()
		t.Fatalf("Update: %v", err)
	}
	cancel()

	out, err := execTeamBoard(t, claim, &teamTaskClaimParams{ID: id, Owner: "alice"})
	if err != nil || !strings.Contains(out, "认领成功") {
		t.Fatalf("Meta 损坏时认领应成功: out=%q err=%v", out, err)
	}
	_, meta := mustLoadTeamTask(t, p, id)
	if meta.Owner != "alice" {
		t.Fatalf("认领后 Meta 应重新写入 owner: %+v", meta)
	}
}
