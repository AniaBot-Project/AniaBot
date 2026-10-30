package tasklog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/storage"
)

// fakeStore 进程内 PersistentStorage 实现，仅用于单测。
type fakeStore struct {
	data map[string]string
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string]string{}} }

func (s *fakeStore) GetString(_ context.Context, key string) (string, bool) {
	v, ok := s.data[key]
	return v, ok
}
func (s *fakeStore) SetString(_ context.Context, key, val string) bool {
	s.data[key] = val
	return true
}
func (s *fakeStore) Get(ctx context.Context, key string, out any) bool {
	v, ok := s.data[key]
	if !ok {
		return false
	}
	return json.Unmarshal([]byte(v), out) == nil
}
func (s *fakeStore) Set(ctx context.Context, key string, val any) bool {
	b, err := json.Marshal(val)
	if err != nil {
		return false
	}
	s.data[key] = string(b)
	return true
}
func (s *fakeStore) Has(_ context.Context, key string) bool { _, ok := s.data[key]; return ok }
func (s *fakeStore) Del(_ context.Context, key string) bool {
	delete(s.data, key)
	return true
}
func (s *fakeStore) Keys(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range s.data {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}
func (s *fakeStore) Clear(_ context.Context) bool                  { s.data = map[string]string{}; return true }
func (s *fakeStore) Clone(prefix string) storage.PersistentStorage { return s } // 测试不复用前缀

func TestRecordAndRecent(t *testing.T) {
	l, _ := newSQLLogger(t)

	l.Record(Entry{TaskID: "t1", Status: StatusSuccess, TaskTitle: "A"})
	l.Record(Entry{TaskID: "t2", Status: StatusTimeout, TaskTitle: "B"})
	l.Record(Entry{TaskID: "t3", Status: StatusError, TaskTitle: "C"})

	got := l.Recent(10)
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d", len(got))
	}
	// newest first：最后写入的 t3 应在最前
	if got[0].TaskID != "t3" || got[2].TaskID != "t1" {
		t.Fatalf("order wrong: %+v", got)
	}
	// ID 自增
	if got[0].ID == got[1].ID || got[1].ID == got[2].ID {
		t.Fatalf("ids not unique: %+v", got)
	}
}

func TestNoEviction(t *testing.T) {
	l, _ := newSQLLogger(t)
	for range 5 {
		l.Record(Entry{TaskID: "t", Status: StatusSuccess})
	}
	got := l.Recent(0)
	if len(got) != 5 {
		t.Fatalf("日志应全部保留，实际 %d 条", len(got))
	}
}

func TestMarkRunningInterrupted(t *testing.T) {
	l, _ := newSQLLogger(t)
	now := time.Now()
	l.Record(Entry{TaskID: "t1", Status: StatusRunning, TriggerTime: now.Add(-time.Minute)})
	l.Record(Entry{TaskID: "t2", Status: StatusSuccess, TriggerTime: now.Add(-3 * time.Minute)})
	l.Record(Entry{TaskID: "t3", Status: StatusRunning, TriggerTime: now.Add(-2 * time.Minute)})
	l.Record(Entry{TaskID: "t4", Status: StatusError, TriggerTime: now.Add(-4 * time.Minute)})

	if n := l.MarkRunningInterrupted(); n != 2 {
		t.Fatalf("want 2 interrupted, got %d", n)
	}
	for _, x := range l.Recent(0) {
		if x.TaskID == "t1" || x.TaskID == "t3" {
			if x.Status != StatusInterrupted || x.Error == "" || x.FinishedAt.IsZero() || x.DurationMs <= 0 {
				t.Fatalf("running 记录未正确标记中断: %+v", x)
			}
		} else if x.Status == StatusRunning {
			t.Fatalf("running 状态应全部被标记: %+v", x)
		}
	}
	// 再次调用无新增更新
	if n := l.MarkRunningInterrupted(); n != 0 {
		t.Fatalf("二次调用应返回 0，实际 %d", n)
	}
}

func TestUpdate(t *testing.T) {
	l, _ := newSQLLogger(t)
	e := l.Record(Entry{TaskID: "t1", Status: StatusRunning})
	l.Update(e.ID, func(en *Entry) {
		en.Status = StatusSuccess
		en.DurationMs = 42
	})
	for _, x := range l.Recent(0) {
		if x.ID == e.ID {
			if x.Status != StatusSuccess || x.DurationMs != 42 {
				t.Fatalf("update not applied: %+v", x)
			}
			return
		}
	}
	t.Fatalf("entry %s not found after update", e.ID)
}

func TestQueryBeforeCursor(t *testing.T) {
	l, _ := newSQLLogger(t)
	for _, title := range []string{"a", "b", "c", "d", "e"} {
		l.Record(Entry{TaskID: "t", Status: StatusSuccess, TaskTitle: title})
	}
	all := l.Query(Filter{Limit: 10})
	if len(all) != 5 {
		t.Fatalf("want 5 entries, got %d", len(all))
	}
	// 以中间记录为游标，应只返回比它更旧的两条
	cursor := all[2].ID
	page := l.Query(Filter{Before: cursor, Limit: 10})
	if len(page) != 2 || page[0].ID != all[3].ID || page[1].ID != all[4].ID {
		t.Fatalf("cursor page wrong: %+v", page)
	}
	// 非法游标不生效，从最新开始
	bad := l.Query(Filter{Before: "!!", Limit: 1})
	if len(bad) != 1 || bad[0].ID != all[0].ID {
		t.Fatalf("invalid cursor should be ignored: %+v", bad)
	}
	// 游标与过滤条件叠加
	l2, _ := newSQLLogger(t)
	l2.Record(Entry{TaskID: "a", Status: StatusSuccess})
	l2.Record(Entry{TaskID: "b", Status: StatusSuccess})
	e3 := l2.Record(Entry{TaskID: "a", Status: StatusTimeout})
	l2.Record(Entry{TaskID: "a", Status: StatusSuccess})
	got := l2.Query(Filter{TaskID: "a", Before: e3.ID, Limit: 10})
	if len(got) != 1 || got[0].TaskID != "a" {
		t.Fatalf("cursor + filter wrong: %+v", got)
	}
}

func TestRecentForTask(t *testing.T) {
	l, _ := newSQLLogger(t)
	l.Record(Entry{TaskID: "a", Status: StatusSuccess})
	l.Record(Entry{TaskID: "b", Status: StatusSuccess})
	l.Record(Entry{TaskID: "a", Status: StatusTimeout})
	got := l.RecentForTask("a", 0)
	if len(got) != 2 {
		t.Fatalf("want 2 for task a, got %d", len(got))
	}
	if got[0].Status != StatusTimeout {
		t.Fatalf("want newest first, got %+v", got)
	}
}
