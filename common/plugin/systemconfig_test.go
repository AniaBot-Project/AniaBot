package plugin

import (
	"reflect"
	"testing"

	"github.com/jeanhua/AniaBot/common/model/message"
)

func TestSystemConfigAdmins(t *testing.T) {
	cfg := SystemConfig{AdminIds: []message.QID{message.FromUint64(1), message.FromUint64(2)}}
	if !reflect.DeepEqual(cfg.Admins(), []message.QID{message.FromUint64(1), message.FromUint64(2)}) {
		t.Fatalf("Admins() = %#v", cfg.Admins())
	}
	if !cfg.IsAdmin(message.FromUint64(1)) || !cfg.IsAdmin(message.FromUint64(2)) {
		t.Fatal("列表中的管理员应全部通过 IsAdmin")
	}
	if cfg.IsAdmin(message.FromUint64(3)) || cfg.IsAdmin("") {
		t.Fatal("非管理员/空 ID 不应通过 IsAdmin")
	}
}

// TestSystemConfigAdminIdFallback 只设置 AdminId 的旧用法仍生效。
func TestSystemConfigAdminIdFallback(t *testing.T) {
	cfg := SystemConfig{AdminId: message.FromUint64(9)}
	if !reflect.DeepEqual(cfg.Admins(), []message.QID{message.FromUint64(9)}) {
		t.Fatalf("Admins() = %#v", cfg.Admins())
	}
	if !cfg.IsAdmin(message.FromUint64(9)) || cfg.IsAdmin(message.FromUint64(8)) {
		t.Fatal("AdminId 回退语义错误")
	}
}

func TestSystemConfigAdminsEmpty(t *testing.T) {
	cfg := SystemConfig{}
	if len(cfg.Admins()) != 0 {
		t.Fatalf("未配置时 Admins() 应为空, got %#v", cfg.Admins())
	}
	if cfg.IsAdmin(message.FromUint64(0)) {
		t.Fatal("未配置时任何 ID 都不应是管理员")
	}
}
