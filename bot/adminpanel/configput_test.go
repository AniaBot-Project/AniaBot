package adminpanel

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeanhua/AniaBot/bot/core/configstore"
)

// TestConfigPutNullDeletesKey 配置更新接口收到 null 值时应删除该键
// （可选参数清空=恢复未配置语义），普通值正常写入。
func TestConfigPutNullDeletesKey(t *testing.T) {
	store := configstore.New(newFakePersistent(), slog.Default())
	s := &Server{opt: Options{Config: store, Logger: slog.Default()}}
	if err := store.Set("plugin.ai_chat_bot.temperature", 1.2); err != nil {
		t.Fatalf("预置配置失败: %v", err)
	}

	// 预置敏感字段，验证掩码占位符走「不修改」而非删除
	if err := store.Set("plugin.ai_chat_bot.api_key", "sk-secret"); err != nil {
		t.Fatalf("预置敏感配置失败: %v", err)
	}

	body := map[string]any{
		"plugin.ai_chat_bot.temperature": nil,
		"plugin.ai_chat_bot.top_p":       0.9,
		"plugin.ai_chat_bot.api_key":     maskPlaceholder,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求失败: %v", err)
	}
	rec := httptest.NewRecorder()
	s.handleConfigPut(rec, httptest.NewRequest("PUT", "/api/config", bytes.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("plugin.ai_chat_bot.temperature"); ok {
		t.Error("temperature 应被删除（null=清空）")
	}
	if v, ok := store.Get("plugin.ai_chat_bot.top_p"); !ok || v.(float64) != 0.9 {
		t.Errorf("top_p 应写入 0.9, got %v, ok=%v", v, ok)
	}
	// 掩码占位符=不修改：api_key 原值应保留，且不能被 null 逻辑误删
	if v, ok := store.Get("plugin.ai_chat_bot.api_key"); !ok || v.(string) != "sk-secret" {
		t.Errorf("api_key 应保持不变, got %v, ok=%v", v, ok)
	}
}

// TestConfigPutRejectsMetaKeys 内部元数据键（meta.*）必须拒绝写入与删除。
//
// 删除 meta.initialized 会让下次启动重新执行 seedDefaults()，而它对模板里每个
// 键都是无条件写入（不是 EnsureDefaults 的"不覆盖"语义），等于把全部用户配置
// 覆盖回默认值——必须从面板入口堵住。
func TestConfigPutRejectsMetaKeys(t *testing.T) {
	store := configstore.New(newFakePersistent(), slog.Default())
	s := &Server{opt: Options{Config: store, Logger: slog.Default()}}
	if err := store.Set("meta.initialized", true); err != nil {
		t.Fatalf("预置 meta 键失败: %v", err)
	}
	if err := store.Set("plugin.ai_chat_bot.model", "keep-me"); err != nil {
		t.Fatalf("预置配置失败: %v", err)
	}

	body := map[string]any{
		"meta.initialized":         nil,   // 试图删除内部标记
		"meta.setup_pending":       false, // 试图写入内部标记
		"plugin.ai_chat_bot.model": "new", // 正常键应照常生效
	}
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	s.handleConfigPut(rec, httptest.NewRequest("PUT", "/api/config", bytes.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if _, ok := store.Get("meta.initialized"); !ok {
		t.Error("meta.initialized 不应被删除（删除会触发默认配置回灌）")
	}
	if _, ok := store.Get("meta.setup_pending"); ok {
		t.Error("meta.setup_pending 不应被写入")
	}
	if v, ok := store.Get("plugin.ai_chat_bot.model"); !ok || v.(string) != "new" {
		t.Errorf("正常配置键应照常生效, got %v, ok=%v", v, ok)
	}
}
