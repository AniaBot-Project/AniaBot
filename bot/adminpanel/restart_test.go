package adminpanel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeanhua/AniaBot/bot/component/sysrestart"
	"github.com/jeanhua/AniaBot/common/workstate"
)

// TestHandleRestartStatusJSON 重启状态接口返回进程实例标识与进行中任务快照。
func TestHandleRestartStatusJSON(t *testing.T) {
	endWork := workstate.Begin(workstate.KindTurn, "AI 对话 g:qq:1")
	defer endWork()

	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleRestartStatus(rec, httptest.NewRequest(http.MethodGet, "/api/restart/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec.Code)
	}
	var st sysrestart.RestartStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, rec.Body.String())
	}
	if st.Boot == "" || st.PID <= 0 {
		t.Fatalf("缺少进程标识: %+v", st)
	}
	if st.Pending {
		t.Fatalf("未请求重启时不应为 pending: %+v", st)
	}
	if st.Busy != 1 || len(st.Items) != 1 || st.Items[0].Kind != workstate.KindTurn {
		t.Fatalf("进行中任务信息错误: %+v", st)
	}
	if st.RemainingSec != -1 {
		t.Fatalf("未等待重启时剩余时间应为 -1, got %d", st.RemainingSec)
	}
}
