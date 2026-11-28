package core

import (
	"testing"

	"github.com/jeanhua/AniaBot/common/workstate"
)

// TestTrackWorkRegistersInFlight core 的 bot.Bot.TrackWork 应把插件一次性后台
// 任务登记到进行中工作表（优雅重启据此等待），结束函数幂等。
func TestTrackWorkRegistersInFlight(t *testing.T) {
	ania := NewAniaBot()
	before := workstate.Snapshot().Count
	end := ania.TrackWork("插件测试任务")
	if got := workstate.Snapshot().Count; got != before+1 {
		t.Fatalf("TrackWork 后应多一个进行中任务: before=%d got=%d", before, got)
	}
	end()
	end() // 幂等
	if got := workstate.Snapshot().Count; got != before {
		t.Fatalf("结束后应恢复原数量: got=%d want=%d", got, before)
	}
}
