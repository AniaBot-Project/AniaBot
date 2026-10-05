package adminpanel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeanhua/AniaBot/bot/marketplace"
)

// marketTestConfig 满足 marketplace.Config 的内存配置。
type marketTestConfig struct{ m map[string]any }

func (c *marketTestConfig) Get(k string) (any, bool) { v, ok := c.m[k]; return v, ok }
func (c *marketTestConfig) Set(k string, v any) error {
	c.m[k] = v
	return nil
}

// TestMarketplaceBatchRequestBatch 批量接口的请求校验与错误路径。
func TestMarketplaceBatchRequestValidation(t *testing.T) {
	mkServer := func(enabled bool) *Server {
		cfg := &marketTestConfig{m: map[string]any{
			"bot.marketplace.enable":     enabled,
			"bot.marketplace.plugin_dir": t.TempDir(),
			"bot.marketplace.cache_dir":  t.TempDir(),
		}}
		return &Server{opt: Options{Marketplace: marketplace.New(cfg, nil)}}
	}

	t.Run("市场不可用", func(t *testing.T) {
		s := &Server{opt: Options{}}
		rec := httptest.NewRecorder()
		s.handleMarketplaceBatch(rec, httptest.NewRequest("POST", "/api/marketplace/batch", strings.NewReader(`{"install":["aa"]}`)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("空列表", func(t *testing.T) {
		s := mkServer(true)
		rec := httptest.NewRecorder()
		s.handleMarketplaceBatch(rec, httptest.NewRequest("POST", "/api/marketplace/batch", strings.NewReader(`{"install":[],"uninstall":[]}`)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("非法 ID", func(t *testing.T) {
		s := mkServer(true)
		body, _ := json.Marshal(map[string]any{"install": []string{"../escape"}})
		rec := httptest.NewRecorder()
		s.handleMarketplaceBatch(rec, httptest.NewRequest("POST", "/api/marketplace/batch", bytes.NewReader(body)))
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("市场未开启", func(t *testing.T) {
		s := mkServer(false)
		body, _ := json.Marshal(map[string]any{"install": []string{"aa"}})
		rec := httptest.NewRecorder()
		s.handleMarketplaceBatch(rec, httptest.NewRequest("POST", "/api/marketplace/batch", bytes.NewReader(body)))
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})
}
