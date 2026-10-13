package executor

import (
	"context"
	"sync"
	"time"
)

const cliDiscoveryRefreshInterval = 3 * time.Minute

// 单次实际探测的期限。等待其他扫描的时间不计入。
var cliDiscoveryBudget = 45 * time.Second

// 测试可替换探测实现，避免刷新测试启动真实 CLI。
var (
	discoverInstalledCLIs = DiscoverCLIs
	refreshUserPath       = refreshDiscoveryPath
	onDiscoveryWait       func()
)

// 发现缓存只保存可用快照；空模型结果允许下一次请求重试。
// 同类探测共享在途结果，不同 CLI 的模型探测互不阻塞。
type cliDiscoveryCache struct {
	mu           sync.RWMutex
	items        []DiscoveredCLI
	models       map[string][]string
	lastSync     time.Time
	refresh      *cliDiscoveryCall
	modelFlights map[string]*modelDiscoveryCall
	generations  map[string]uint64
	persist      *discoveryPersist
}

type cliDiscoveryCall struct {
	done        chan struct{}
	err         error
	refreshPath bool
}

type modelDiscoveryCall struct {
	done       chan struct{}
	models     []string
	generation uint64
}

var defaultCLIDiscovery = &cliDiscoveryCache{models: map[string][]string{}}

// refreshMu 防止周期任务重叠；手动检测共享同一轮实际探测。
var refreshMu sync.Mutex

func StartCLIDiscoveryRefresh(ctx context.Context, configDir string) {
	persist := newDiscoveryPersist(configDir)
	cache := defaultCLIDiscovery
	cache.mu.Lock()
	cache.persist = persist
	cache.mu.Unlock()

	// 在后台探测开始前载入快照，避免冷启动请求与快照写回互相覆盖。
	if items, ok := persist.loadCLIs(); ok {
		cache.setItems(items)
	}
	if models, ok := persist.loadModels(); ok {
		cache.setModels(models)
	}
	go persist.runWriteQueue(ctx)
	go func() {
		refreshCLIAndModels(ctx)
		ticker := time.NewTicker(cliDiscoveryRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshCLIAndModels(ctx)
			}
		}
	}()
}

func refreshCLIAndModels(ctx context.Context) {
	if !refreshMu.TryLock() {
		return
	}
	defer refreshMu.Unlock()
	if refreshCLIDiscovery(ctx, false) != nil {
		return
	}
	var wg sync.WaitGroup
	for _, item := range GetCLIDiscovery(ctx) {
		if !item.Installed {
			continue
		}
		wg.Add(1)
		go func(cliType string) {
			defer wg.Done()
			RefreshCLIModels(ctx, cliType)
		}(item.Type)
	}
	wg.Wait()
}

func GetCLIDiscovery(ctx context.Context) []DiscoveredCLI {
	cache := defaultCLIDiscovery
	cache.mu.RLock()
	ready := !cache.lastSync.IsZero()
	items := cloneCLIList(cache.items)
	cache.mu.RUnlock()
	if ready {
		return items
	}
	_ = RefreshCLIDiscovery(ctx)
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cloneCLIList(cache.items)
}

func GetCLIModels(ctx context.Context, cliType string) []string {
	return defaultCLIDiscovery.probeModels(ctx, cliType, false)
}

// RefreshCLIModels 绕过缓存；暂时探测失败时保留同一安装版本的可用列表。
func RefreshCLIModels(ctx context.Context, cliType string) []string {
	return defaultCLIDiscovery.probeModels(ctx, cliType, true)
}

func (c *cliDiscoveryCache) probeModels(ctx context.Context, cliType string, force bool) []string {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	c.mu.Lock()
	if !force && len(c.models[cliType]) > 0 {
		models := cloneModels(c.models[cliType])
		c.mu.Unlock()
		return models
	}
	if call := c.modelFlights[cliType]; call != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			models, _ := c.getModel(cliType)
			return models
		case <-call.done:
			return cloneModels(call.models)
		}
	}
	if ctx.Err() != nil {
		models := cloneModels(c.models[cliType])
		c.mu.Unlock()
		return models
	}
	if c.modelFlights == nil {
		c.modelFlights = make(map[string]*modelDiscoveryCall)
	}
	call := &modelDiscoveryCall{done: make(chan struct{}), generation: c.generations[cliType]}
	c.modelFlights[cliType] = call
	c.mu.Unlock()

	models := DiscoverCLIModels(ctx, cliType)
	c.mu.Lock()
	// 安装状态/版本变更或请求取消后，不让旧探测覆盖新的缓存。
	if ctx.Err() == nil && call.generation == c.generations[cliType] && len(models) > 0 {
		if c.models == nil {
			c.models = make(map[string][]string)
		}
		c.models[cliType] = cloneModels(models)
	}
	call.models = cloneModels(c.models[cliType])
	if c.modelFlights[cliType] == call {
		delete(c.modelFlights, cliType)
	}
	close(call.done)
	c.mu.Unlock()
	c.persistSnapshot()
	return cloneModels(call.models)
}

// RefreshCLIDiscovery 完成实际检测后才返回；并发调用等待同一轮结果。
func RefreshCLIDiscovery(ctx context.Context) error {
	return refreshCLIDiscovery(ctx, true)
}

func refreshCLIDiscovery(ctx context.Context, refreshPath bool) error {
	return refreshCLIDiscoveryOnce(ctx, refreshPath, true)
}

// allowRetry 只补一轮：等待到的扫描没有刷新 PATH，或对方已取消而本次请求仍有效。
// 再失败就返回，避免后台扫描持续超时时来回重试。
func refreshCLIDiscoveryOnce(ctx context.Context, refreshPath bool, allowRetry bool) error {
	cache := defaultCLIDiscovery
	cache.mu.Lock()
	if call := cache.refresh; call != nil {
		cache.mu.Unlock()
		if onDiscoveryWait != nil {
			onDiscoveryWait()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-call.done:
			if allowRetry && shouldRediscover(ctx, refreshPath, call) {
				return refreshCLIDiscoveryOnce(ctx, refreshPath, false)
			}
			return call.err
		}
	}
	if err := ctx.Err(); err != nil {
		cache.mu.Unlock()
		return err
	}
	// 期限从真正开始探测时计算。前面的等待不能吃掉手动刷新的 45 秒。
	runCtx, cancel := context.WithTimeout(ctx, cliDiscoveryBudget)
	defer cancel()
	call := &cliDiscoveryCall{done: make(chan struct{}), refreshPath: refreshPath}
	cache.refresh = call
	cache.mu.Unlock()

	if refreshPath {
		refreshUserPath(runCtx)
	}
	items := discoverInstalledCLIs(runCtx)
	err := runCtx.Err()
	if err == nil {
		cache.setItems(items)
		cache.persistSnapshot()
	}
	cache.mu.Lock()
	call.err = err
	cache.refresh = nil
	close(call.done)
	cache.mu.Unlock()
	return err
}

func shouldRediscover(ctx context.Context, refreshPath bool, call *cliDiscoveryCall) bool {
	if ctx.Err() != nil {
		return false
	}
	if refreshPath && !call.refreshPath {
		return true
	}
	// 对方的取消或超时不属于本次请求；本次 context 仍有效时再探测一轮。
	return call.err != nil
}

func cloneCLIList(items []DiscoveredCLI) []DiscoveredCLI {
	out := make([]DiscoveredCLI, len(items))
	copy(out, items)
	for i := range out {
		out[i].Models = cloneModels(out[i].Models)
	}
	return out
}

func cloneModels(models []string) []string {
	return append([]string{}, models...)
}

func (c *cliDiscoveryCache) getPersist() *discoveryPersist {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.persist
}

func (c *cliDiscoveryCache) setItems(items []DiscoveredCLI) {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := make(map[string]DiscoveredCLI, len(c.items))
	for _, item := range c.items {
		previous[item.Type] = item
	}
	next := cloneCLIList(items)
	for i, item := range next {
		old, exists := previous[item.Type]
		delete(previous, item.Type)
		// --version 暂时失败不能让同一安装的可用模型丢失。
		if exists && item.Installed && old.Installed && item.ExecPath == old.ExecPath && item.Version == "" {
			next[i].Version = old.Version
		}
		if !item.Installed || (exists && (old.Installed != item.Installed || old.ExecPath != item.ExecPath || old.Version != next[i].Version)) {
			c.invalidateModelLocked(item.Type)
		}
	}
	for cliType := range previous {
		c.invalidateModelLocked(cliType)
	}
	c.items = next
	c.lastSync = time.Now()
}

func (c *cliDiscoveryCache) invalidateModelLocked(cliType string) {
	delete(c.models, cliType)
	// 新安装不能等待旧安装的在途请求；旧请求仍按自己的 context 有界退出。
	delete(c.modelFlights, cliType)
	if c.generations == nil {
		c.generations = make(map[string]uint64)
	}
	c.generations[cliType]++
}

func (c *cliDiscoveryCache) setModels(models map[string][]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = make(map[string][]string, len(models))
	for cliType, list := range models {
		if len(list) > 0 {
			c.models[cliType] = cloneModels(list)
		}
	}
}

func (c *cliDiscoveryCache) getModel(cliType string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	models, ok := c.models[cliType]
	return cloneModels(models), ok
}

func (c *cliDiscoveryCache) persistSnapshot() {
	p := c.getPersist()
	if p == nil {
		return
	}
	c.mu.RLock()
	items := cloneCLIList(c.items)
	models := make(map[string][]string, len(c.models))
	for k, v := range c.models {
		models[k] = cloneModels(v)
	}
	// 排队动作也在快照锁内，防止旧快照延迟入队覆盖较新的快照。
	p.saveLatest(items, models)
	c.mu.RUnlock()
}
