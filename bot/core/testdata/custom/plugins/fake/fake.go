// Package fake 测试用插件包（位于 testdata 下，不参与构建）。
package fake

import (
	"context"

	"github.com/jeanhua/AniaBot/common/plugin"
)

type FakePlugin struct {
	plugin.Meta
	Calls []plugin.UnloadReason
}

func (p *FakePlugin) OnUnload(ctx context.Context, reason plugin.UnloadReason) error {
	p.Calls = append(p.Calls, reason)
	return nil
}
