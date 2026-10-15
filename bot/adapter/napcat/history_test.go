package napcat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// TestHistoryParamsBackwardCursor 校验带游标的历史请求声明了向后回溯方向。
// NapCat 缺省按「锚点向更新方向」取窗口，不声明 reverse_order 时传游标只会原地返回
// 同一段最新消息（NapCat issue #441），「向前翻页」永远无效。
func TestHistoryParamsBackwardCursor(t *testing.T) {
	// 群：带游标 → 两个键名都发（兼容 2025-11 前后两代协议端的键名）
	got := historyParams("group_id", "qq:835747109", 25, 861745537)
	if got["group_id"] != "835747109" {
		t.Errorf("群号应去掉 qq: 前缀, got %v", got["group_id"])
	}
	if got["count"] != 25 || got["message_seq"] != 861745537 {
		t.Errorf("条数/游标透传不符: %v", got)
	}
	if got["reverse_order"] != true || got["reverseOrder"] != true {
		t.Errorf("带游标时必须声明两代键名的回溯方向, got %v", got)
	}

	// 好友：键名为 user_id
	friend := historyParams("user_id", "qq:123456", 10, 42)
	if friend["user_id"] != "123456" {
		t.Errorf("好友号应去掉 qq: 前缀, got %v", friend["user_id"])
	}
	if friend["reverse_order"] != true || friend["reverseOrder"] != true {
		t.Errorf("好友历史同样需要声明回溯方向, got %v", friend)
	}
}

// TestHistoryParamsNoCursorOmitsDirection 无游标时不发方向参数：NapCat 该分支走
// getAioFirstViewLatestMsgs（取最新若干条），方向参数不参与，发了反而增加噪音。
func TestHistoryParamsNoCursorOmitsDirection(t *testing.T) {
	got := historyParams("group_id", "qq:1", 20, 0)
	if _, ok := got["reverse_order"]; ok {
		t.Errorf("无游标不应携带 reverse_order: %v", got)
	}
	if _, ok := got["reverseOrder"]; ok {
		t.Errorf("无游标不应携带 reverseOrder: %v", got)
	}
	if got["message_seq"] != 0 {
		t.Errorf("游标应原样透传 0, got %v", got["message_seq"])
	}
}

// TestHttpGetGroupMsgHistorySendsReverseOrder 端到端锁定 HTTP 适配器实际发出的请求体：
// 带游标时必须含 reverse_order/reverseOrder，否则 NapCat 不会向前回溯。
func TestHttpGetGroupMsgHistorySendsReverseOrder(t *testing.T) {
	var (
		mu   sync.Mutex
		body map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_group_msg_history" {
			http.NotFound(w, r)
			return
		}
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("请求体解析失败: %v", err)
		}
		mu.Lock()
		body = got
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"messages":[]}}`))
	}))
	defer srv.Close()

	a, ok := NewNapcatHttpAdapter().(*napcatHttpAdapter)
	if !ok {
		t.Fatal("NewNapcatHttpAdapter 应返回 *napcatHttpAdapter")
	}
	a.baseUrl = srv.URL
	a.reqTimeout = 3 * time.Second
	if _, ok := a.GetGroupMsgHistory(message.QID("qq:835747109"), 25, 861745537); !ok {
		t.Fatal("历史查询应成功")
	}

	mu.Lock()
	defer mu.Unlock()
	if body["group_id"] != "835747109" {
		t.Errorf("群号应去前缀, got %v", body["group_id"])
	}
	// JSON 数字解码为 float64
	if body["message_seq"] != float64(861745537) {
		t.Errorf("游标应透传, got %v", body["message_seq"])
	}
	for _, key := range []string{"reverse_order", "reverseOrder"} {
		if body[key] != true {
			t.Errorf("请求体必须含 %s=true（否则 NapCat 无法向前回溯）, body=%v", key, body)
		}
	}
}
