package pluginaichat

import (
	"context"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/llmtool"
)

func TestTodoReplaceValidation(t *testing.T) {
	longContent := strings.Repeat("很", todoMaxContentRunes+1)
	tooMany := make([]todoItem, todoMaxItems+1)
	for i := range tooMany {
		tooMany[i] = todoItem{Content: "任务", Status: todoStatusPending}
	}

	cases := []struct {
		name    string
		items   []todoItem
		wantErr string
	}{
		{"正常清单", []todoItem{{Content: "a", Status: "pending"}, {Content: "b", Status: "in_progress"}}, ""},
		{"空列表合法（清空）", nil, ""},
		{"空内容", []todoItem{{Content: "  ", Status: "pending"}}, "content 不能为空"},
		{"超长内容", []todoItem{{Content: longContent, Status: "pending"}}, "上限"},
		{"非法状态", []todoItem{{Content: "a", Status: "doing"}}, "status 非法"},
		{"状态必填", []todoItem{{Content: "a"}}, "status 非法"},
		{"多个进行中", []todoItem{{Content: "a", Status: "in_progress"}, {Content: "b", Status: "in_progress"}}, "只能有 1 项 in_progress"},
		{"数量超限", tooMany, "任务数量超限"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTodoManager()
			err := m.replace("g:1", tc.items)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("不应报错: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("期望错误含 %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestTodoReplaceAtomicOnError 校验失败时旧清单保持不变（全量替换语义）。
func TestTodoReplaceAtomicOnError(t *testing.T) {
	m := newTodoManager()
	old := []todoItem{{Content: "旧任务", Status: "pending"}}
	if err := m.replace("g:1", old); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := m.replace("g:1", []todoItem{{Content: "", Status: "pending"}}); err == nil {
		t.Fatal("应报错")
	}
	if got := m.lists["g:1"]; len(got) != 1 || got[0].Content != "旧任务" {
		t.Fatalf("校验失败不应改动旧清单, got %+v", got)
	}
}

func TestTodoWriteToolExecute(t *testing.T) {
	m := newTodoManager()
	tool := newTodoWriteTool(m, "g:1")
	if tool.Name() != "todo_write" {
		t.Fatalf("工具名 = %q", tool.Name())
	}

	res, err := tool.Execute(context.Background(), &todoWriteParams{Todos: []todoItem{
		{Content: "整理数据", Status: "completed"},
		{Content: "写报告", Status: "in_progress", ActiveForm: "正在写报告"},
		{Content: "发送结果", Status: "pending"},
	}}, llmtool.CallBackFuncs{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res, "共 3 项（进行中 1，待办 1，已完成 1）") {
		t.Fatalf("统计行不符: %s", res)
	}
	if !strings.Contains(res, "2. [进行中] 写报告") {
		t.Fatalf("编号清单不符: %s", res)
	}

	res, err = tool.Execute(context.Background(), &todoWriteParams{}, llmtool.CallBackFuncs{})
	if err != nil || res != "任务清单已清空" {
		t.Fatalf("清空结果不符: res=%q err=%v", res, err)
	}
	if len(m.lists) != 0 {
		t.Fatal("清空后不应残留清单")
	}

	// 校验失败走 Go error（由工具执行层回填给 AI）
	if _, err = tool.Execute(context.Background(), &todoWriteParams{Todos: []todoItem{{Content: "x"}}}, llmtool.CallBackFuncs{}); err == nil {
		t.Fatal("非法参数应返回 error")
	}
}

// TestTodoPendingReminderDedup 提醒注入按内容哈希去重：清单没变不重复注入；
// 全部完成/清空后复位，再次新增时重新注入。
func TestTodoPendingReminderDedup(t *testing.T) {
	m := newTodoManager()
	const key = "g:1"

	if got := m.pendingReminder(key); got != "" {
		t.Fatalf("无清单时不应提醒, got %q", got)
	}

	_ = m.replace(key, []todoItem{
		{Content: "已完成的事", Status: "completed"},
		{Content: "待办的事", Status: "pending"},
	})
	first := m.pendingReminder(key)
	if first == "" || !strings.Contains(first, "1 项未完成") || !strings.Contains(first, "待办的事") {
		t.Fatalf("首次提醒不符: %q", first)
	}
	if strings.Contains(first, "已完成的事") {
		t.Fatalf("提醒不应包含已完成项: %q", first)
	}
	if got := m.pendingReminder(key); got != "" {
		t.Fatalf("清单未变不应重复提醒, got %q", got)
	}

	// 清单变化 → 重新注入
	_ = m.replace(key, []todoItem{{Content: "待办的事", Status: "in_progress"}})
	if got := m.pendingReminder(key); got == "" || !strings.Contains(got, "进行中") {
		t.Fatalf("清单变化后应重新提醒, got %q", got)
	}

	// 全部完成 → 不再提醒且哈希复位
	_ = m.replace(key, []todoItem{{Content: "待办的事", Status: "completed"}})
	if got := m.pendingReminder(key); got != "" {
		t.Fatalf("全部完成后不应提醒, got %q", got)
	}
	_ = m.replace(key, []todoItem{{Content: "待办的事", Status: "completed"}, {Content: "新任务", Status: "pending"}})
	if got := m.pendingReminder(key); got == "" {
		t.Fatal("完成复位后新增任务应重新提醒")
	}
}

// TestTodoReminderCommitsOnlyAfterSuccess 提醒哈希只在请求成功后提交：
// 生成时即提交会让一次失败（超时/取消/报错）吞掉后续所有轮次的提醒——而清单
// 未变时恰恰是最需要提醒的时候。
func TestTodoReminderCommitsOnlyAfterSuccess(t *testing.T) {
	m := newTodoManager()
	const key = "g:1"
	_ = m.replace(key, []todoItem{{Content: "编译并测试", Status: "pending"}})

	// 生成提醒（未提交：模拟本轮请求尚未成功返回）
	if got := m.pendingReminder(key); got == "" {
		t.Fatal("首轮应生成提醒")
	}
	// 同一轮内重复调用不应重复注入
	if got := m.pendingReminder(key); got != "" {
		t.Fatalf("同一轮内重复调用不应重复注入, got %q", got)
	}
	m.commitReminder(key) // 请求成功 → 提交

	// 已提交：清单未变则不再注入
	if got := m.pendingReminder(key); got != "" {
		t.Fatalf("已成功提醒后不应重复注入, got %q", got)
	}

	// 清单变化 → 重新提醒，再提交后再次静默
	_ = m.replace(key, []todoItem{{Content: "编译并测试", Status: "in_progress"}})
	if got := m.pendingReminder(key); got == "" {
		t.Fatal("清单变化后应重新提醒")
	}
	if got := m.pendingReminder(key); got != "" {
		t.Fatalf("提交前同一轮不应重复注入, got %q", got)
	}
	m.commitReminder(key)
	if got := m.pendingReminder(key); got != "" {
		t.Fatalf("提交后不应重复注入, got %q", got)
	}

	// 提交幂等：重复调用不改变状态、不 panic
	m.commitReminder(key)
	m.commitReminder(key)
	if got := m.pendingReminder(key); got != "" {
		t.Fatalf("重复提交后仍不应注入, got %q", got)
	}

	// 无待提交哈希时提交为无害空操作
	m.commitReminder("g:不存在")
}

// TestTodoReminderResetsWhenAllCompleted 全部完成后哈希复位：新增任务能重新提醒，
// 且 pending 归零时不得留下待提交残留导致下一轮静默。
func TestTodoReminderResetsWhenAllCompleted(t *testing.T) {
	m := newTodoManager()
	const key = "g:1"
	_ = m.replace(key, []todoItem{{Content: "任务A", Status: "pending"}})
	if got := m.pendingReminder(key); got == "" {
		t.Fatal("应生成提醒")
	}
	m.commitReminder(key)

	// 全部完成 → 复位
	_ = m.replace(key, []todoItem{{Content: "任务A", Status: "completed"}})
	if got := m.pendingReminder(key); got != "" {
		t.Fatalf("全部完成后不应提醒, got %q", got)
	}
	// 新增任务 → 即使内容与历史提醒完全相同（同文本哈希）也应重新提醒
	_ = m.replace(key, []todoItem{{Content: "任务A", Status: "completed"}, {Content: "任务A", Status: "pending"}})
	if got := m.pendingReminder(key); got == "" {
		t.Fatal("复位后新增任务应重新提醒（哈希未复位会被静默吞掉）")
	}
}
