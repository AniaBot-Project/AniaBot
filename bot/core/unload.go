package core

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jeanhua/AniaBot/common/plugin"
	"github.com/jeanhua/AniaBot/common/pluginmeta"
)

// UnloadEventTimeout 单个插件卸载钩子的执行超时（与其它生命周期事件一致）。
const UnloadEventTimeout = time.Minute

// markUnloaded 尝试把插件标记为已触发卸载钩子；重复标记返回 false。
// 市场卸载按插件单独触发后，退出/重启清扫时不再重复调用。
func (ania *AniaBot) markUnloaded(name string) bool {
	ania.unloadMu.Lock()
	defer ania.unloadMu.Unlock()
	if _, ok := ania.unloadedPlugins[name]; ok {
		return false
	}
	ania.unloadedPlugins[name] = struct{}{}
	return true
}

// unloadPlugins 调用实现了 plugin.UnloadEvent 的插件的卸载钩子。
// 按插件启动顺序的逆序执行（后启动的先清理），每个插件最多调用一次；
// 钩子出错或 panic 只记日志，不阻断其它插件的清理。
func (ania *AniaBot) unloadPlugins(reason plugin.UnloadReason) {
	if !ania.pluginsStarted.Load() {
		// 插件尚未完成 Start（如构造后直接 Stop），不触发钩子
		return
	}
	for i := len(ania.plugins) - 1; i >= 0; i-- {
		p := ania.plugins[i]
		u, ok := p.(plugin.UnloadEvent)
		if !ok || !ania.markUnloaded(p.GetMeta().Name) {
			continue
		}
		safeExecute("卸载", p, func(p plugin.Plugin) {
			ctx, cancel := context.WithTimeout(context.Background(), UnloadEventTimeout)
			defer cancel()
			Logger().Info("触发插件卸载钩子", "plugin", p.GetMeta().Name, "reason", reason)
			logError(u.OnUnload(ctx, reason), p, "卸载")
		})
	}
}

// UnloadPlugin 调用插件市场中 id 对应插件的卸载钩子（reason=UnloadUninstall）。
// 供插件市场在卸载流水线中调用（core.AniaBot 实现 marketplace.PluginUnloader）。
// called=false 表示插件未实现卸载钩子（无需清理）；找不到运行实例或钩子
// 执行失败时返回错误。
func (ania *AniaBot) UnloadPlugin(ctx context.Context, id string) (called bool, err error) {
	if !ania.pluginsStarted.Load() {
		return false, nil
	}
	p := ania.findMarketplacePlugin(id)
	if p == nil {
		return false, fmt.Errorf("未找到插件 %s 的运行实例", id)
	}
	u, ok := p.(plugin.UnloadEvent)
	if !ok {
		return false, nil
	}
	if !ania.markUnloaded(p.GetMeta().Name) {
		return true, nil // 已触发过（如退出清扫先到），不重复调用
	}
	Logger().Info("触发插件卸载钩子", "plugin", p.GetMeta().Name, "reason", plugin.UnloadUninstall)
	hookCtx, cancel := context.WithTimeout(ctx, UnloadEventTimeout)
	defer cancel()
	hookErr, panicked := safeExecuteWithReturn("卸载", p, func(plugin.Plugin) error {
		return u.OnUnload(hookCtx, plugin.UnloadUninstall)
	})
	if panicked {
		return true, fmt.Errorf("插件 %s 卸载钩子执行 panic", id)
	}
	if hookErr != nil {
		return true, fmt.Errorf("插件 %s 卸载钩子执行失败: %w", id, hookErr)
	}
	return true, nil
}

// findMarketplacePlugin 按插件市场 ID 查找运行中的插件实例：市场插件由
// plugingen 生成注册代码，实例类型的包路径固定位于 custom/plugins/<id>
// 下（类型定义在插件子包时同样命中），按包路径匹配（同 ID 唯一，
// 市场插件目录名即 ID）。
func (ania *AniaBot) findMarketplacePlugin(id string) plugin.Plugin {
	for _, p := range ania.plugins {
		if isMarketplacePlugin(pluginPkgPath(p), id) {
			return p
		}
	}
	return nil
}

// pluginPkgPath 插件实例具体类型的包路径（指针解引用；匿名类型返回空串）。
func pluginPkgPath(p plugin.Plugin) string {
	t := reflect.TypeOf(p)
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.PkgPath()
}

// isMarketplacePlugin 包路径是否对应插件市场插件 id：路径中最后一个
// custom/plugins/ 之后的部分等于 id（类型在本包），或以 id + "/" 开头
// （类型定义在插件的子包）。
func isMarketplacePlugin(pkgPath, id string) bool {
	if id == "" {
		return false
	}
	marker := "/" + pluginmeta.PluginRoot + "/"
	idx := strings.LastIndex(pkgPath, marker)
	if idx < 0 {
		return false
	}
	rest := pkgPath[idx+len(marker):]
	return rest == id || strings.HasPrefix(rest, id+"/")
}
