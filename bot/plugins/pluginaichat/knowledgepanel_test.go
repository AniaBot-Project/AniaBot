package pluginaichat

import (
	"io"
	"log/slog"
	"testing"

	"github.com/jeanhua/AniaBot/common/plugininfo"
)

// newTestKnowledgePlugin 构造一个挂载了知识库管理器的插件实例（maxDocs=0 不限条数）
func newTestKnowledgePlugin() *AIChatPlugin {
	p := &AIChatPlugin{knowledgeManager: newTestKnowledgeManager(0)}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return p
}

func TestKnowledgePanelCRUD(t *testing.T) {
	p := newTestKnowledgePlugin()

	// 新增
	id, err := p.KnowledgeCreate(plugininfo.KnowledgeDocUpsert{
		Scope:   "global",
		Title:   "Docker 部署指南",
		Content: "第一步：安装 docker。",
		Tags:    []string{"部署"},
		Source:  "url:https://example.com",
	})
	if err != nil {
		t.Fatalf("KnowledgeCreate 失败: %v", err)
	}
	if id == "" {
		t.Fatal("KnowledgeCreate 未返回 ID")
	}

	// scope 列表
	scopes := p.KnowledgeScopes()
	if len(scopes) != 1 {
		t.Fatalf("scope 数量不符: %+v", scopes)
	}
	s := scopes[0]
	if s.Scope != "global" || s.Kind != "global" || s.Count != 1 {
		t.Fatalf("scope 信息不符: %+v", s)
	}

	// 列表
	docs, err := p.KnowledgeList("global")
	if err != nil {
		t.Fatalf("KnowledgeList 失败: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != id || docs[0].Title != "Docker 部署指南" || docs[0].Source != "url:https://example.com" {
		t.Fatalf("文档列表不符: %+v", docs)
	}

	// 编辑
	if err := p.KnowledgeUpdate(plugininfo.KnowledgeDocUpsert{
		Scope:   "global",
		ID:      id,
		Title:   "部署指南（更新）",
		Content: "第二步：docker compose up -d。",
		Tags:    []string{"docker"},
	}); err != nil {
		t.Fatalf("KnowledgeUpdate 失败: %v", err)
	}
	docs, _ = p.KnowledgeList("global")
	if len(docs) != 1 || docs[0].Title != "部署指南（更新）" || docs[0].Source != "" {
		t.Fatalf("更新后内容不符: %+v", docs)
	}

	// 删除
	if err := p.KnowledgeDelete("global", id); err != nil {
		t.Fatalf("KnowledgeDelete 失败: %v", err)
	}
	if got := p.KnowledgeScopes(); len(got) != 0 {
		t.Fatalf("删除全部文档后不应残留作用域: %+v", got)
	}
	if err := p.KnowledgeDelete("global", id); err == nil {
		t.Fatal("删除不存在的 ID 应报错")
	}
}

func TestKnowledgePanelScopeValidation(t *testing.T) {
	p := newTestKnowledgePlugin()

	for _, bad := range []string{"", "g:", "f:", "x:123", "abc", "123", "../admin", "Global", "GLOBAL", "global "} {
		if _, err := p.KnowledgeList(bad); err == nil {
			t.Fatalf("非法 scope %q 应被拒绝（KnowledgeList）", bad)
		}
		if _, err := p.KnowledgeCreate(plugininfo.KnowledgeDocUpsert{Scope: bad, Content: "x"}); err == nil {
			t.Fatalf("非法 scope %q 应被拒绝（KnowledgeCreate）", bad)
		}
		if err := p.KnowledgeDelete(bad, "deadbeef"); err == nil {
			t.Fatalf("非法 scope %q 应被拒绝（KnowledgeDelete）", bad)
		}
	}

	// 多平台合法 scope：global、QQ 数字 ID 与带平台前缀的 ID（如飞书 fs:）均接受
	for _, good := range []string{"global", "g:123", "f:456", "g:fs:oc_xxx", "f:fs:ou_xxx"} {
		if _, err := p.KnowledgeList(good); err != nil {
			t.Fatalf("合法 scope %q 应被接受（KnowledgeList）: %v", good, err)
		}
		if _, err := p.KnowledgeCreate(plugininfo.KnowledgeDocUpsert{Scope: good, Content: "x"}); err != nil {
			t.Fatalf("合法 scope %q 应被接受（KnowledgeCreate）: %v", good, err)
		}
	}

	// 更新时 id 为空应报错
	if err := p.KnowledgeUpdate(plugininfo.KnowledgeDocUpsert{Scope: "global", Content: "x"}); err == nil {
		t.Fatal("KnowledgeUpdate 缺少 id 应报错")
	}
	// URL 导入校验
	if _, err := p.KnowledgeImportURL("global", "not-a-url"); err == nil {
		t.Fatal("非法 URL 应被拒绝")
	}
}

func TestKnowledgePanelDisabled(t *testing.T) {
	p := &AIChatPlugin{}
	p.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	if got := p.KnowledgeScopes(); got != nil {
		t.Fatalf("功能未启用时 KnowledgeScopes 应返回 nil，实际 %v", got)
	}
	if _, err := p.KnowledgeList("global"); err == nil {
		t.Fatal("功能未启用时 KnowledgeList 应报错")
	}
	if _, err := p.KnowledgeCreate(plugininfo.KnowledgeDocUpsert{Scope: "global", Content: "x"}); err == nil {
		t.Fatal("功能未启用时 KnowledgeCreate 应报错")
	}
	if err := p.KnowledgeUpdate(plugininfo.KnowledgeDocUpsert{Scope: "global", ID: "x", Content: "y"}); err == nil {
		t.Fatal("功能未启用时 KnowledgeUpdate 应报错")
	}
	if err := p.KnowledgeDelete("global", "x"); err == nil {
		t.Fatal("功能未启用时 KnowledgeDelete 应报错")
	}
	if _, err := p.KnowledgeImportURL("global", "https://example.com"); err == nil {
		t.Fatal("功能未启用时 KnowledgeImportURL 应报错")
	}
}

func TestScopeKind(t *testing.T) {
	cases := map[string]string{
		"global": "global",
		"g:123":  "group",
		"f:456":  "friend",
		"other":  "unknown",
	}
	for scope, want := range cases {
		if got := scopeKind(scope); got != want {
			t.Fatalf("scopeKind(%q) = %q，期望 %q", scope, got, want)
		}
	}
}
