package core

import (
	"reflect"
	"testing"

	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/spf13/viper"
)

func TestAdminIDsFromConfig(t *testing.T) {
	v := viper.New()
	v.Set("bot.admin_ids", []string{"qq:1", " 2 ", "qq:1", ""})
	got := adminIDsFromConfig(v)
	want := []message.QID{message.FromString("qq:1"), message.FromString("2")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adminIDsFromConfig = %#v, want %#v", got, want)
	}
}

// TestAdminIDsFromConfigLegacyFallback 只有旧版单值键 bot.admin_id 时回退读取。
func TestAdminIDsFromConfigLegacyFallback(t *testing.T) {
	v := viper.New()
	v.Set("bot.admin_id", "345")
	got := adminIDsFromConfig(v)
	want := []message.QID{message.FromUint64(345)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adminIDsFromConfig = %#v, want %#v", got, want)
	}
}

// TestAdminIDsFromConfigEmpty 未配置时可多个键均为空，返回空列表。
func TestAdminIDsFromConfigEmpty(t *testing.T) {
	if got := adminIDsFromConfig(viper.New()); len(got) != 0 {
		t.Fatalf("未配置管理员时应返回空列表, got %#v", got)
	}
	if got := adminIDsFromConfig(nil); len(got) != 0 {
		t.Fatalf("nil viper 应返回空列表, got %#v", got)
	}
}
