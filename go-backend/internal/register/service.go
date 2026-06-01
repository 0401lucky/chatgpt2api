package register

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	registerModeTotal     = "total"
	registerModeQuota     = "quota"
	registerModeAvailable = "available"
)

type AccountProvider interface {
	ListAccounts() []map[string]any
	AddAccounts(tokens []string) map[string]any
	RefreshAccounts(ctx context.Context, tokens []string) map[string]any
}

type registerWorkerResult struct {
	ok     bool
	index  int
	result map[string]any
	err    string
	cost   float64
}

type Service struct {
	mu          sync.Mutex
	path        string
	accounts    AccountProvider
	mail        *mailProviderFactory
	config      map[string]any
	logs        []map[string]any
	subscribers map[chan string]struct{}
	runnerAlive bool
}

func NewService(path, reputationPath string, accounts AccountProvider) *Service {
	s := &Service{
		path:        path,
		accounts:    accounts,
		mail:        &mailProviderFactory{Reputation: newDomainReputationStore(reputationPath)},
		config:      registerDefaultConfig(),
		subscribers: map[chan string]struct{}{},
	}
	s.config = s.load()
	if boolValue(s.config["enabled"], false) {
		s.startLocked(false)
	}
	return s
}

func (s *Service) Get() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Service) Update(updates map[string]any) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = normalizeRegisterConfig(mergeMap(s.config, updates))
	s.saveLocked()
	s.notifyLocked()
	return s.snapshotLocked()
}

func (s *Service) Start() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startLocked(true)
	return s.snapshotLocked()
}

func (s *Service) Stop() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config["enabled"] = false
	stats := asMap(s.config["stats"])
	stats["updated_at"] = nowISO()
	s.config["stats"] = stats
	s.appendLogLocked("已请求停止注册任务，正在等待当前运行任务结束", "yellow")
	s.saveLocked()
	s.notifyLocked()
	return s.snapshotLocked()
}

func (s *Service) Reset() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = nil
	s.config["stats"] = registerZeroStats(intValue(s.config["threads"], 1), s.poolMetricsLocked())
	s.saveLocked()
	s.notifyLocked()
	return s.snapshotLocked()
}

func (s *Service) Subscribe(ctx context.Context) <-chan string {
	ch := make(chan string, 8)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	initial := s.snapshotJSONLocked()
	s.mu.Unlock()
	ch <- initial
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.subscribers, ch)
		s.mu.Unlock()
		close(ch)
	}()
	return ch
}

func (s *Service) SnapshotJSON() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotJSONLocked()
}

func (s *Service) startLocked(resetLogs bool) {
	if s.runnerAlive {
		s.config["enabled"] = true
		s.saveLocked()
		s.notifyLocked()
		return
	}
	if resetLogs {
		s.logs = nil
	}
	s.config["enabled"] = true
	stats := registerZeroStats(intValue(s.config["threads"], 1), s.poolMetricsLocked())
	stats["job_id"] = newHex(32)
	stats["started_at"] = nowISO()
	stats["updated_at"] = nowISO()
	s.config["stats"] = stats
	s.saveLocked()
	s.runnerAlive = true
	s.notifyLocked()
	mode := clean(s.config["mode"])
	if mode == "" {
		mode = registerModeTotal
	}
	s.appendLogLocked(fmt.Sprintf("注册任务启动，模式=%s，线程数=%d", mode, intValue(s.config["threads"], 1)), "yellow")
	go s.run()
}

func (s *Service) run() {
	cfg := s.Get()
	threads := maxInt(1, intValue(cfg["threads"], 1))
	submitted, running, done, success, fail := 0, 0, 0, 0, 0
	results := make(chan registerWorkerResult, threads)
	for {
		current := s.Get()
		for boolValue(current["enabled"], false) && !s.targetReached(current, submitted) && running < threads {
			submitted++
			running++
			workerCfg := cloneMap(current)
			workerCfg["mail"] = cloneMap(asMap(current["mail"]))
			go func(index int, config map[string]any) {
				results <- s.runWorker(index, config)
			}(submitted, workerCfg)
			current = s.Get()
		}
		s.bumpStats(map[string]any{"running": running, "done": done, "success": success, "fail": fail})
		if running == 0 {
			mode := clean(current["mode"])
			if !boolValue(current["enabled"], false) || mode == "" || mode == registerModeTotal {
				break
			}
			time.Sleep(time.Duration(maxInt(1, intValue(current["check_interval"], 5))) * time.Second)
			continue
		}
		res := <-results
		running--
		done++
		if res.ok {
			success++
		} else {
			fail++
		}
	}
	s.bumpStats(map[string]any{"running": 0, "done": done, "success": success, "fail": fail, "finished_at": nowISO()})
	s.mu.Lock()
	s.runnerAlive = false
	s.config["enabled"] = false
	s.saveLocked()
	s.notifyLocked()
	s.appendLogLocked(fmt.Sprintf("注册任务结束，成功%d，失败%d", success, fail), "yellow")
	s.mu.Unlock()
}

func (s *Service) runWorker(index int, config map[string]any) registerWorkerResult {
	start := time.Now()
	worker, err := newRegisterWorker(s, index, config)
	if err != nil {
		s.appendLog(fmt.Sprintf("任务%d 初始化失败，原因: %v", index, err), "red")
		return registerWorkerResult{ok: false, index: index, err: err.Error(), cost: time.Since(start).Seconds()}
	}
	defer worker.close()
	s.appendLog(fmt.Sprintf("[任务%d] 任务启动", index), "")
	result, runErr := worker.run(context.Background())
	cost := time.Since(start).Seconds()
	if runErr != nil {
		s.handleRegisterFailure(runErr)
		s.appendLog(fmt.Sprintf("任务%d 注册失败，本次耗时%.1fs，原因: %v", index, cost, runErr), "red")
		return registerWorkerResult{ok: false, index: index, err: runErr.Error(), cost: cost}
	}
	accessToken := clean(result["access_token"])
	if accessToken == "" {
		err = fmt.Errorf("register flow did not return access_token")
		s.appendLog(fmt.Sprintf("任务%d 注册失败，本次耗时%.1fs，原因: %v", index, cost, err), "red")
		return registerWorkerResult{ok: false, index: index, err: err.Error(), cost: cost}
	}
	if s.accounts != nil {
		s.accounts.AddAccounts([]string{accessToken})
		s.accounts.RefreshAccounts(context.Background(), []string{accessToken})
	}
	s.handleRegisterSuccess(result)
	s.appendLog(fmt.Sprintf("%s 注册成功，本次耗时%.1fs", clean(result["email"]), cost), "green")
	return registerWorkerResult{ok: true, index: index, result: result, cost: cost}
}

func (s *Service) handleRegisterSuccess(result map[string]any) {
	provider := clean(result["mail_provider"])
	domain := clean(result["mail_domain"])
	if provider != "" && domain != "" {
		s.mail.Reputation.RecordSuccess(provider, domain)
	}
}

func (s *Service) handleRegisterFailure(err error) {
	attempt, ok := err.(*attemptError)
	if !ok {
		return
	}
	provider := attempt.Provider()
	domain := attempt.Domain()
	if provider == "" || domain == "" {
		return
	}
	s.mail.Reputation.RecordFailure(provider, domain, attempt.Reason)
}

func (s *Service) appendLog(text, level string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendLogLocked(text, level)
}

func (s *Service) appendLogLocked(text, level string) {
	item := map[string]any{"time": nowISO(), "text": text, "level": firstNonEmpty(level, "info")}
	s.logs = append(s.logs, item)
	if len(s.logs) > 300 {
		s.logs = append([]map[string]any(nil), s.logs[len(s.logs)-300:]...)
	}
	s.notifyLocked()
}

func (s *Service) snapshotLocked() map[string]any {
	out := cloneMap(s.config)
	out["mail"] = cloneMap(asMap(s.config["mail"]))
	stats := cloneMap(asMap(s.config["stats"]))
	for key, value := range s.poolMetricsLocked() {
		stats[key] = value
	}
	out["stats"] = stats
	logs := make([]map[string]any, len(s.logs))
	for i, item := range s.logs {
		logs[i] = cloneMap(item)
	}
	out["logs"] = logs
	return out
}

func (s *Service) snapshotJSONLocked() string {
	data, err := json.Marshal(s.snapshotLocked())
	if err != nil {
		return "{}"
	}
	return string(data)
}

func (s *Service) notifyLocked() {
	payload := s.snapshotJSONLocked()
	for ch := range s.subscribers {
		select {
		case ch <- payload:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- payload:
			default:
			}
		}
	}
}

func (s *Service) poolMetricsLocked() map[string]any {
	if s.accounts == nil {
		return map[string]any{"current_quota": 0, "current_available": 0}
	}
	items := s.accounts.ListAccounts()
	quota := 0
	available := 0
	for _, item := range items {
		if clean(item["status"]) != "正常" {
			continue
		}
		available++
		if !boolValue(item["image_quota_unknown"], false) {
			quota += intValue(item["quota"], 0)
		}
	}
	return map[string]any{"current_quota": quota, "current_available": available}
}

func (s *Service) targetReached(cfg map[string]any, submitted int) bool {
	metrics := s.poolMetrics()
	s.bumpStats(metrics)
	mode := clean(cfg["mode"])
	switch mode {
	case registerModeQuota:
		reached := intValue(metrics["current_quota"], 0) >= intValue(cfg["target_quota"], 1)
		s.appendLog(fmt.Sprintf("检查号池：当前正常账号=%d，当前剩余额度=%d，目标额度=%d，%s", intValue(metrics["current_available"], 0), intValue(metrics["current_quota"], 0), intValue(cfg["target_quota"], 1), registerSkipText(reached)), "yellow")
		return reached
	case registerModeAvailable:
		reached := intValue(metrics["current_available"], 0) >= intValue(cfg["target_available"], 1)
		s.appendLog(fmt.Sprintf("检查号池：当前正常账号=%d，目标账号=%d，当前剩余额度=%d，%s", intValue(metrics["current_available"], 0), intValue(cfg["target_available"], 1), intValue(metrics["current_quota"], 0), registerSkipText(reached)), "yellow")
		return reached
	default:
		return submitted >= intValue(cfg["total"], 1)
	}
}

func registerSkipText(reached bool) string {
	if reached {
		return "跳过注册"
	}
	return "继续注册"
}

func (s *Service) poolMetrics() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.poolMetricsLocked()
}

func (s *Service) bumpStats(updates map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := asMap(s.config["stats"])
	for key, value := range updates {
		stats[key] = value
	}
	if startedAt := clean(stats["started_at"]); startedAt != "" {
		if started, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
			elapsed := round1(time.Since(started).Seconds())
			stats["elapsed_seconds"] = elapsed
			success := intValue(stats["success"], 0)
			fail := intValue(stats["fail"], 0)
			if success > 0 {
				stats["avg_seconds"] = round1(elapsed / float64(success))
			} else {
				stats["avg_seconds"] = 0
			}
			stats["success_rate"] = round1(float64(success) * 100 / float64(maxInt(1, success+fail)))
		}
	}
	stats["updated_at"] = nowISO()
	s.config["stats"] = stats
	s.saveLocked()
	s.notifyLocked()
}

func (s *Service) load() map[string]any {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return normalizeRegisterConfig(nil)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return normalizeRegisterConfig(nil)
	}
	return normalizeRegisterConfig(data)
}

func (s *Service) saveLocked() {
	if s.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	raw, err := json.MarshalIndent(s.config, "", "  ")
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	_ = os.WriteFile(s.path, raw, 0o644)
}

func registerDefaultConfig() map[string]any {
	stats := registerZeroStats(64, map[string]any{"current_quota": 0, "current_available": 0})
	return map[string]any{
		"mail": map[string]any{
			"request_timeout": 15,
			"wait_timeout":    30,
			"wait_interval":   3,
			"providers":       []map[string]any{},
		},
		"proxy":            "",
		"total":            20000,
		"threads":          64,
		"mode":             registerModeTotal,
		"target_quota":     100,
		"target_available": 10,
		"check_interval":   5,
		"enabled":          false,
		"stats":            stats,
	}
}

func registerZeroStats(threads int, metrics map[string]any) map[string]any {
	return map[string]any{
		"success":           0,
		"fail":              0,
		"done":              0,
		"running":           0,
		"threads":           maxInt(1, threads),
		"elapsed_seconds":   0,
		"avg_seconds":       0,
		"success_rate":      0,
		"current_quota":     intValue(metrics["current_quota"], 0),
		"current_available": intValue(metrics["current_available"], 0),
		"updated_at":        nowISO(),
	}
}

func normalizeRegisterConfig(raw map[string]any) map[string]any {
	cfg := registerDefaultConfig()
	for key, value := range raw {
		if key == "stats" || key == "logs" {
			continue
		}
		cfg[key] = value
	}
	cfg["proxy"] = strings.TrimSpace(clean(cfg["proxy"]))
	cfg["total"] = maxInt(1, intValue(cfg["total"], 1))
	cfg["threads"] = maxInt(1, intValue(cfg["threads"], 1))
	mode := clean(cfg["mode"])
	if mode != registerModeQuota && mode != registerModeAvailable {
		mode = registerModeTotal
	}
	cfg["mode"] = mode
	cfg["target_quota"] = maxInt(1, intValue(cfg["target_quota"], 1))
	cfg["target_available"] = maxInt(1, intValue(cfg["target_available"], 1))
	cfg["check_interval"] = maxInt(1, intValue(cfg["check_interval"], 5))
	cfg["enabled"] = boolValue(cfg["enabled"], false)
	cfg["mail"] = normalizeRegisterMailConfig(asMap(cfg["mail"]))
	stats := registerZeroStats(intValue(cfg["threads"], 1), map[string]any{
		"current_quota":     intValue(asMap(raw["stats"])["current_quota"], 0),
		"current_available": intValue(asMap(raw["stats"])["current_available"], 0),
	})
	for key, value := range asMap(raw["stats"]) {
		stats[key] = value
	}
	stats["threads"] = intValue(cfg["threads"], 1)
	cfg["stats"] = stats
	cfg["logs"] = trimLogs(asMapSlice(raw["logs"]), 300)
	return cfg
}

func normalizeRegisterMailConfig(raw map[string]any) map[string]any {
	cfg := map[string]any{
		"request_timeout": maxInt(1, intValue(raw["request_timeout"], 15)),
		"wait_timeout":    maxInt(1, intValue(raw["wait_timeout"], 30)),
		"wait_interval":   maxInt(1, intValue(raw["wait_interval"], 3)),
		"user_agent":      firstNonEmpty(clean(raw["user_agent"]), registerUserAgent),
	}
	providers := asMapSlice(raw["providers"])
	out := make([]map[string]any, 0, len(providers))
	for _, provider := range providers {
		item := copyMap(provider)
		item["type"] = clean(item["type"])
		item["enable"] = boolValue(item["enable"], false)
		if item["domain"] != nil {
			item["domain"] = asStringSlice(item["domain"])
		}
		if item["subdomain"] != nil {
			item["subdomain"] = clean(item["subdomain"])
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		out = []map[string]any{{"enable": true, "type": "tempmail_lol", "domain": []any{}}}
	}
	cfg["providers"] = out
	return cfg
}
