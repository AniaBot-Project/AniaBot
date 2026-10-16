package pluginaichat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeanhua/AniaBot/common/model/message"
)

// denyExistLockFake 可注入失败的锁存储：验证续期失败/令牌被换的锁丢失自检。
type denyExistLockFake struct {
	*lockFake
	mu sync.Mutex

	// expireFails 为 true 时 Expire 恒失败（模拟存储故障）
	expireFails bool
	// dropOnGet 为 true 时 GetString 报键不存在（模拟键过期/被删）
	dropOnGet bool
	// stealTo 非空时 GetString 返回该令牌（模拟锁被他人接管）
	stealTo string
}

func newDenyExistLockFake() *denyExistLockFake {
	return &denyExistLockFake{lockFake: newLockFake()}
}

func (f *denyExistLockFake) GetString(ctx context.Context, key string) (string, bool) {
	f.mu.Lock()
	steal := f.stealTo
	drop := f.dropOnGet
	f.mu.Unlock()
	if drop {
		return "", false
	}
	if steal != "" {
		return steal, true
	}
	return f.lockFake.GetString(ctx, key)
}

func (f *denyExistLockFake) Expire(ctx context.Context, key string, ttl time.Duration) bool {
	f.mu.Lock()
	fail := f.expireFails
	f.mu.Unlock()
	if fail {
		return false
	}
	return f.lockFake.Expire(ctx, key, ttl)
}

// TestTryLockDetailedRateLimited 并发槽位满时失败原因必须是 lockRateLimited：
// 该原因下调用方直接拒绝消息而不是排队（排队会因没有消费者而沉淀）。
func TestTryLockDetailedRateLimited(t *testing.T) {
	p := newEvictTestPlugin()
	p.rateCh = make(chan struct{}, 1)

	lock, fail := p.tryLockDetailed(message.FromUint64(1), true)
	if lock == nil || fail != lockOK {
		t.Fatalf("首个请求应成功: lock=%v fail=%v", lock, fail)
	}
	defer lock.release()

	// 槽位被占（且不同会话锁空闲）：必须报告并发受限，而不是「会话忙」
	lock2, fail2 := p.tryLockDetailed(message.FromUint64(2), true)
	if lock2 != nil {
		t.Fatal("槽位已满时不应拿到锁")
	}
	if fail2 != lockRateLimited {
		t.Fatalf("失败原因应为 lockRateLimited，实际 %v", fail2)
	}
}

// TestTryLockDetailedBusy 会话锁已被持有时报告 lockBusy（消息应排队）。
func TestTryLockDetailedBusy(t *testing.T) {
	p := newEvictTestPlugin()
	p.rateCh = make(chan struct{}, 4)

	lock, fail := p.tryLockDetailed(message.FromUint64(1), true)
	if lock == nil || fail != lockOK {
		t.Fatalf("首个请求应成功: %v", fail)
	}
	lock2, fail2 := p.tryLockDetailed(message.FromUint64(1), true)
	if lock2 != nil || fail2 != lockBusy {
		t.Fatalf("同会话并发应报告 lockBusy: lock=%v fail=%v", lock2, fail2)
	}
	lock.release()

	// 释放后并发槽位归还，且锁可重新获取（验证 release 的删键/还槽顺序）
	lock3, fail3 := p.tryLockDetailed(message.FromUint64(1), true)
	if lock3 == nil || fail3 != lockOK {
		t.Fatalf("释放后应可重新获取锁: %v", fail3)
	}
	lock3.release()
	select {
	case <-p.rateCh:
		t.Fatal("release 后槽位应已归还")
	default:
	}
}

// TestSessionLockRenewLostCancels 锁确认丢失（令牌被换）时取消附着的请求，
// 并退出心跳循环；不 cancel 会让新旧持锁者并发驱动同一 ChatBot。
func TestSessionLockRenewLostCancels(t *testing.T) {
	p := newEvictTestPlugin()
	fake := newDenyExistLockFake()
	p.lockStorage = fake

	lock, fail := p.tryLockDetailed(message.FromUint64(1), true)
	if lock == nil || fail != lockOK {
		t.Fatalf("获取锁失败: %v", fail)
	}

	cancelled := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	lock.attachCancel(func() {
		cancel()
		close(cancelled)
	})

	// 模拟锁被他人接管
	fake.mu.Lock()
	fake.stealTo = "other-token"
	fake.mu.Unlock()

	// 直接驱动心跳逻辑（避免测试等待真实 ticker 间隔）
	if res := lock.renewOnce(); res != renewLost {
		t.Fatalf("令牌被换应报告 renewLost，实际 %v", res)
	}
	lock.lostCancel()

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("锁丢失后应取消附着的请求")
	}
	if ctx.Err() == nil {
		t.Fatal("请求上下文应已被取消")
	}
	lock.release()
}

// TestSessionLockRenewToleratesTransientFailure 瞬时故障（键暂不可见）容忍一次，
// 连续两次才判定丢失：一次存储抖动不应误杀正在进行的响应。
func TestSessionLockRenewToleratesTransientFailure(t *testing.T) {
	p := newEvictTestPlugin()
	fake := newDenyExistLockFake()
	p.lockStorage = fake

	lock, fail := p.tryLockDetailed(message.FromUint64(1), true)
	if lock == nil || fail != lockOK {
		t.Fatalf("获取锁失败: %v", fail)
	}
	defer lock.release()

	fake.mu.Lock()
	fake.dropOnGet = true
	fake.mu.Unlock()

	if res := lock.renewOnce(); res != renewMiss {
		t.Fatalf("键暂不可见应报告 renewMiss，实际 %v", res)
	}
	fake.mu.Lock()
	fake.dropOnGet = false
	fake.mu.Unlock()
	if res := lock.renewOnce(); res != renewOK {
		t.Fatalf("恢复后应续期成功，实际 %v", res)
	}
}

// TestStopRequestAuthorization 只有触发请求的用户或管理员能停止；被拒时不动请求。
func TestStopRequestAuthorization(t *testing.T) {
	p := newEvictTestPlugin()
	p.SystemConfig.AdminId = message.FromUint64(900)

	requester := message.FromUint64(100)
	ctx, cancel := context.WithCancel(context.Background())
	p.setActiveContext(message.FromUint64(1), true, &activeRequest{cancel: cancel, requester: requester})

	// 无关群成员：拒绝，请求继续
	if stopped, denied := p.stopRequest(message.FromUint64(1), true, message.FromUint64(200), false); stopped || denied == "" {
		t.Fatalf("无关用户应被拒绝: stopped=%v denied=%q", stopped, denied)
	}
	if ctx.Err() != nil {
		t.Fatal("被拒的停止请求不应取消上下文")
	}
	if _, ok := p.activeContexts.Load(sessionKey(message.FromUint64(1), true)); !ok {
		t.Fatal("被拒的停止请求不应移除活跃请求")
	}

	// 管理员：放行
	if stopped, denied := p.stopRequest(message.FromUint64(1), true, message.FromUint64(900), true); !stopped || denied != "" {
		t.Fatalf("管理员应可停止: stopped=%v denied=%q", stopped, denied)
	}
	if ctx.Err() == nil {
		t.Fatal("管理员停止后上下文应被取消")
	}
}

// TestStopRequestRequesterSelf 触发者本人可停止自己的请求。
func TestStopRequestRequesterSelf(t *testing.T) {
	p := newEvictTestPlugin()
	requester := message.FromUint64(100)
	ctx, cancel := context.WithCancel(context.Background())
	p.setActiveContext(message.FromUint64(2), true, &activeRequest{cancel: cancel, requester: requester})

	if stopped, denied := p.stopRequest(message.FromUint64(2), true, requester, false); !stopped || denied != "" {
		t.Fatalf("触发者应可停止自己的请求: stopped=%v denied=%q", stopped, denied)
	}
	if ctx.Err() == nil {
		t.Fatal("上下文应被取消")
	}
}

// TestStopRequestNoActive 无活跃请求时不报权限错误、不 panic。
func TestStopRequestNoActive(t *testing.T) {
	p := newEvictTestPlugin()
	if stopped, denied := p.stopRequest(message.FromUint64(1), true, message.FromUint64(1), false); stopped || denied != "" {
		t.Fatalf("无活跃请求应返回 (false, \"\"): stopped=%v denied=%q", stopped, denied)
	}
}

// TestBatchRequester 取批内最后一位真实发言者，跳过合成消息（UserId=0）。
func TestBatchRequester(t *testing.T) {
	batch := []message.Message{
		{Sender: message.MessageSender{UserId: message.FromUint64(11)}},
		{Sender: message.MessageSender{UserId: message.FromUint64(0)}}, // 子代理结果
		{Sender: message.MessageSender{UserId: message.FromUint64(22)}},
		{Sender: message.MessageSender{UserId: message.FromUint64(0)}},
	}
	if got := batchRequester(batch); got != message.FromUint64(22) {
		t.Fatalf("应取最后一位真实发言者 22，实际 %v", got)
	}
	if got := batchRequester([]message.Message{{Sender: message.MessageSender{UserId: message.FromUint64(0)}}}); got != message.FromUint64(0) {
		t.Fatalf("全为合成消息时应返回 0，实际 %v", got)
	}
}

// TestApprovalWaitBudgetShrinksToContext 审批等待压缩到父上下文剩余预算之内，
// 保证超时被拒后仍有预算向用户说明结论。
func TestApprovalWaitBudgetShrinksToContext(t *testing.T) {
	logger := testLogger()
	m := newApprovalManager(nil, 120, message.FromUint64(0), logger)

	// 无 deadline：用配置超时
	if got := m.approvalWaitBudget(context.Background()); got != 120*time.Second {
		t.Fatalf("无 deadline 时应使用配置超时，实际 %v", got)
	}

	// 剩余 50s：预算 = 50s - 20s 收尾预留 = 30s
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	if got := m.approvalWaitBudget(ctx); got > 30*time.Second || got <= 0 {
		t.Fatalf("应按剩余预算收缩到 ~30s，实际 %v", got)
	}

	// 剩余不足预留：预算为 0 或负（调用方据此不发起审批）
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if got := m.approvalWaitBudget(ctx2); got > 0 {
		t.Fatalf("剩余不足预留时应返回 <=0，实际 %v", got)
	}
}

// TestApprovalRequestWithoutBudget 剩余预算不足时直接拒绝且不登记待批请求。
func TestApprovalRequestWithoutBudget(t *testing.T) {
	logger := testLogger()
	m := newApprovalManager(nil, 120, message.FromUint64(0), logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prompted := false
	allowed, reason := m.request(ctx, "g:1", "bash", "ls", message.FromUint64(100), func(string) { prompted = true })
	if allowed {
		t.Fatal("预算不足时不应放行")
	}
	if !strings.Contains(reason, "剩余时间不足") {
		t.Fatalf("应提示剩余时间不足，实际 %q", reason)
	}
	if prompted {
		t.Fatal("预算不足时不应发送审批提示")
	}
	if _, ok := m.pending["g:1"]; ok {
		t.Fatal("预算不足时不应登记待批请求")
	}
}

// TestApprovalRequestPromptShowsActualWait 审批提示中的等待秒数与实际预算一致
// （收缩后不能让用户以为还有完整 120s）。
func TestApprovalRequestPromptShowsActualWait(t *testing.T) {
	logger := testLogger()
	m := newApprovalManager(nil, 120, message.FromUint64(0), logger)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel() // 预算 = 60s - 20s = 40s

	var prompt string
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.request(ctx, "g:1", "bash", "ls", message.FromUint64(100), func(text string) {
			prompt = text
			cancel() // 立即取消，避免测试等满预算
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("审批请求未按取消返回")
	}
	if !strings.Contains(prompt, "40 秒") {
		t.Fatalf("提示应展示实际等待秒数（40），实际 %q", prompt)
	}
}

// TestWaitSecondsRounding 展示秒数四舍五入：按上下文预算反推的等待时长略小于整秒
// （40s 实为 39.99…s），截断会把提示里的「40 秒」显示成「39 秒」。
func TestWaitSecondsRounding(t *testing.T) {
	if got := waitSeconds(40*time.Second - time.Microsecond); got != 40 {
		t.Fatalf("39.999999 秒应显示 40，实际 %d", got)
	}
	if got := waitSeconds(39*time.Second + 400*time.Millisecond); got != 39 {
		t.Fatalf("39.4 秒应显示 39，实际 %d", got)
	}
}

// TestEvictClearsSessionState 会话淘汰时一并复位计划模式与任务清单
// （两者都声明「随会话淘汰清空」，此前只丢 ChatBot 会留下陈旧状态）。
func TestEvictClearsSessionState(t *testing.T) {
	p := newEvictTestPlugin()
	p.planManager = newPlanManager()
	p.todoManager = newTodoManager()

	id := message.FromUint64(1006)
	key := "g:qq:1006"
	storeIdleEntry(p, key, id, true, 3*time.Hour)
	p.planManager.Set(key, true)
	if err := p.todoManager.replace(key, []todoItem{{Content: "写文档", Status: todoStatusPending}}); err != nil {
		t.Fatalf("写入清单失败: %v", err)
	}

	p.evictChats(2*time.Hour, 0)

	if _, ok := p.chats.Load(key); ok {
		t.Fatal("闲置会话应被淘汰")
	}
	if p.planManager.IsOn(key) {
		t.Fatal("淘汰后计划模式应复位")
	}
	if reminder := p.todoManager.pendingReminder(key); reminder != "" {
		t.Fatalf("淘汰后清单应清空，实际残留提醒: %q", reminder)
	}
}

// TestAsyncSubagentGroupNotRecreatedAfterStop /stop 清空组后，仍在跑的
// 子代理结束时不重建空组（此前 getAsyncGroup 会把空组写回 sync.Map）。
func TestAsyncSubagentGroupNotRecreatedAfterStop(t *testing.T) {
	p := newEvictTestPlugin()
	id := message.FromUint64(1007)

	g := p.getAsyncGroup(id, true)
	g.add("1", func() {}, "长任务")
	p.cancelAsyncSubagents(id, true) // LoadAndDelete + cancelAll

	// 子代理结束路径：只移除、不创建
	p.removeAsyncSubagent(id, true, "1")
	if _, ok := p.asyncSubagents.Load(sessionKey(id, true)); ok {
		t.Fatal("子代理结束后不应残留（或重建）会话组")
	}

	// 查询路径同样不应创建
	if got := p.listRunningSubagents(id, true); len(got) != 0 {
		t.Fatalf("无组时应返回空列表，实际 %d 条", len(got))
	}
	if _, ok := p.asyncSubagents.Load(sessionKey(id, true)); ok {
		t.Fatal("查询不应创建会话组")
	}
	if p.cancelSubagentByID(id, true, "x") {
		t.Fatal("无组时取消应返回 false")
	}
}

// TestAsyncSubagentGroupRemovedWhenDrained 正常结束后组内条目清空、
// sync.Map 条目一并回收。
func TestAsyncSubagentGroupRemovedWhenDrained(t *testing.T) {
	p := newEvictTestPlugin()
	id := message.FromUint64(1008)
	g := p.getAsyncGroup(id, true)
	g.add("1", func() {}, "任务")
	g.add("2", func() {}, "任务2")

	p.removeAsyncSubagent(id, true, "1")
	if _, ok := p.asyncSubagents.Load(sessionKey(id, true)); !ok {
		t.Fatal("仍有运行中子代理时不应移除组")
	}
	p.removeAsyncSubagent(id, true, "2")
	if _, ok := p.asyncSubagents.Load(sessionKey(id, true)); ok {
		t.Fatal("全部结束后应移除 sync.Map 条目")
	}
}

// TestKnowledgeSearchCacheInvalidation 检索缓存：命中缓存不重复读存储，
// 写入/删除后失效（保证面板与 AI 写入立即可见）。
func TestKnowledgeSearchCacheInvalidation(t *testing.T) {
	store := newSQLPFake()
	km := newTestKnowledgeManagerWith(store, 0)

	if _, err := km.add("g:1", "缓存测试", "docker 部署第一步", nil, "manual"); err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	// 首次检索填充缓存
	if got := km.searchImpl("g:1", "docker 部署", 5, nil); len(got) == 0 {
		t.Fatal("应检索到文档")
	}
	if _, ok := km.cache["g:1"]; !ok {
		t.Fatal("检索后应有缓存条目")
	}

	// 直接在存储层写入（绕过管理器）：缓存命中时不应看见——证明检索走缓存
	km.mu.Lock()
	km.store.insert("g:1", kbDoc{ID: "direct01", Scope: "g:1", Title: "绕过缓存的文档", Content: "kafka 集群运维", CreatedAt: time.Now()})
	km.mu.Unlock()
	if got := km.searchImpl("g:1", "kafka 集群运维", 5, nil); len(got) != 0 {
		t.Fatalf("缓存应屏蔽底层直写（检索未走缓存？），实际返回 %d 条", len(got))
	}

	// 经管理器的写入使缓存失效，新文档立即可检索
	if _, err := km.add("g:1", "kafka 运维", "kafka 集群运维手册", nil, "manual"); err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	if got := km.searchImpl("g:1", "kafka 集群运维", 5, nil); len(got) == 0 {
		t.Fatal("写入后缓存应失效，新文档应可检索")
	}
}

// TestKnowledgeSearchCacheEviction FIFO 容量：超过上限时淘汰最旧 scope。
func TestKnowledgeSearchCacheEviction(t *testing.T) {
	km := newTestKnowledgeManager(0)
	km.mu.Lock()
	km.cache = map[string][]kbDoc{}
	km.cacheOrder = nil
	for i := 0; i < kbSearchCacheMaxScopes+2; i++ {
		scope := "g:" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		km.putCacheLocked(scope, []kbDoc{{ID: scope, Scope: scope, Title: "t", Content: "c"}})
	}
	size := len(km.cache)
	order := len(km.cacheOrder)
	km.mu.Unlock()
	if size != kbSearchCacheMaxScopes || order != kbSearchCacheMaxScopes {
		t.Fatalf("缓存应限制在 %d 条，实际 cache=%d order=%d", kbSearchCacheMaxScopes, size, order)
	}
}

// TestMemoryEmbedOutsideLock 记忆写入的向量计算在锁外：模拟慢 embedding 服务时，
// 其他 goroutine 仍能读取记忆（全局锁未被占住）。
func TestMemoryEmbedOutsideLock(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-release // 首次请求挂起，模拟慢 embedding 服务
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"model":"test-embed"}`))
	}))
	defer srv.Close()

	store := newSQLPFake()
	logger := testLogger()
	m := newMemoryManager(store, logger, 0, newEmbedder(srv.URL, "test-key", "test-embed", nil, logger))
	if m == nil {
		t.Fatal("memory manager 初始化失败")
	}
	if m.embedder == nil {
		t.Fatal("embedder 应已启用")
	}
	// 预置一条记忆供并发读取（此时 embedding 服务尚未被挂起？会挂起——用 release 前先放行一次）
	// 简化：直接向 store 写入，避免依赖 embedder
	_ = m.store.insert("g:1", memoryEntry{ID: "seed0001", Content: "既有记忆", CreatedAt: time.Now().UTC()})

	addDone := make(chan error, 1)
	go func() {
		_, _, err := m.addEntry("g:1", "", "新记忆", nil)
		addDone <- err
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("embedding 未被调用")
	}
	// embedding 阻塞中：读路径必须仍然可用（锁未被占住）
	readDone := make(chan []memoryEntry, 1)
	go func() { readDone <- m.list("g:1") }()
	select {
	case got := <-readDone:
		if len(got) == 0 {
			t.Fatal("读取应看到预置记忆")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("embedding 阻塞期间读取被锁住：向量计算仍在锁内")
	}
	close(release)
	select {
	case err := <-addDone:
		if err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("写入未完成")
	}
}

// TestParseSessionKey 会话键解析：g:/f: 前缀与带冒号的平台 ID（如 fs:oc_xxx）。
func TestParseSessionKey(t *testing.T) {
	id, isGroup, ok := parseSessionKey("g:qq:123")
	if !ok || !isGroup || id.String() != "qq:123" {
		t.Fatalf("解析 g:qq:123 失败: id=%v isGroup=%v ok=%v", id, isGroup, ok)
	}
	id, isGroup, ok = parseSessionKey("f:fs:ou_abc")
	if !ok || isGroup || id.String() != "fs:ou_abc" {
		t.Fatalf("解析 f:fs:ou_abc 失败: id=%v isGroup=%v ok=%v", id, isGroup, ok)
	}
	if _, _, ok := parseSessionKey("bad"); ok {
		t.Fatal("非法键应返回 ok=false")
	}
	if _, _, ok := parseSessionKey("x:1"); ok {
		t.Fatal("未知前缀应返回 ok=false")
	}
}

// TestSweepPendingSessionsNoBot 清扫器在缺少平台外观时安全跳过（不消费队列、
// 不 panic）：消息保留给下次正常流程。
func TestSweepPendingSessionsNoBot(t *testing.T) {
	p := newEvictTestPlugin()
	p.ready = true
	p.lockStorage = newLockFake()
	p.rateCh = make(chan struct{}, 4)

	id := message.FromUint64(2001)
	p.enqueuePending(id, true, message.Message{MessageId: message.FromUint64(1)})
	// 会话条目存在但没有 bot 外观
	storeIdleEntry(p, sessionKey(id, true), id, true, 0)

	p.sweepPendingSessions()

	if !p.hasPending(sessionKey(id, true)) {
		t.Fatal("拿不到平台外观时不应消费队列")
	}
}

// TestSweepPendingSessionsNotReady 未就绪（降载模式）时清扫器直接跳过。
func TestSweepPendingSessionsNotReady(t *testing.T) {
	p := newEvictTestPlugin()
	p.ready = false
	id := message.FromUint64(2002)
	p.enqueuePending(id, true, message.Message{MessageId: message.FromUint64(2)})
	p.sweepPendingSessions()
	if !p.hasPending(sessionKey(id, true)) {
		t.Fatal("未就绪时不应消费队列")
	}
}
