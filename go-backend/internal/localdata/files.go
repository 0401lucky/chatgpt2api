package localdata

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type ConfigProvider interface {
	PublicConfig() map[string]any
	Update(map[string]any) (map[string]any, error)
	BackupSettings(maskSecrets bool) map[string]any
	ImgbedSettings(maskSecrets bool) map[string]any
	BackupState() map[string]any
	SaveBackupState(map[string]any) map[string]any
	ImagesDir() string
	ImageThumbnailsDir() string
	ImageHistoryDir() string
	ImageHistoryFile() string
}

type AccountProvider interface {
	ListAccounts() []map[string]any
	AddAccounts(tokens []string) map[string]any
	RefreshAccounts(ctx context.Context, tokens []string) map[string]any
}

type Services struct {
	Config     ConfigProvider
	Accounts   AccountProvider
	DataDir    string
	ProjectDir string
}

func NewServices(cfg ConfigProvider, projectDir, dataDir string, accounts AccountProvider) *Services {
	return &Services{Config: cfg, Accounts: accounts, ProjectDir: projectDir, DataDir: dataDir}
}

func (s *Services) Logs() *LogService {
	return &LogService{Path: filepath.Join(s.DataDir, "logs.jsonl")}
}

func (s *Services) Images() *ImageService {
	return &ImageService{
		ImagesDir:     s.Config.ImagesDir(),
		ThumbnailsDir: s.Config.ImageThumbnailsDir(),
		HistoryFile:   s.Config.ImageHistoryFile(),
		TagsFile:      filepath.Join(s.DataDir, "image_tags.json"),
	}
}

func (s *Services) Register() *RegisterService {
	return &RegisterService{Path: filepath.Join(s.DataDir, "register.json"), Accounts: s.Accounts}
}

func (s *Services) Backup() *BackupService {
	return &BackupService{Config: s.Config, ProjectDir: s.ProjectDir, DataDir: s.DataDir}
}

func (s *Services) CPA() *CPAService {
	return &CPAService{Path: filepath.Join(s.DataDir, "cpa_config.json")}
}

func (s *Services) Sub2API() *Sub2APIService {
	return &Sub2APIService{Path: filepath.Join(s.DataDir, "sub2api_config.json")}
}

type LogService struct {
	Path string
	mu   sync.Mutex
}

func (l *LogService) Add(logType, summary string, detail map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(l.Path), 0o755)
	item := map[string]any{
		"id":      newID(),
		"time":    time.Now().Format("2006-01-02 15:04:05"),
		"type":    logType,
		"summary": summary,
		"detail":  detail,
	}
	data, _ := json.Marshal(item)
	file, err := os.OpenFile(l.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(data, '\n'))
}

func (l *LogService) List(logType, startDate, endDate string, limit int) []map[string]any {
	if limit < 1 {
		limit = 200
	}
	data, err := os.ReadFile(l.Path)
	if err != nil {
		return []map[string]any{}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	items := make([]map[string]any, 0)
	for i := len(lines) - 1; i >= 0; i-- {
		rawLine := strings.TrimSpace(lines[i])
		if rawLine == "" {
			continue
		}
		var item map[string]any
		if json.Unmarshal([]byte(rawLine), &item) != nil {
			continue
		}
		if clean(item["id"]) == "" {
			item["id"] = legacyLogID(rawLine, i)
		}
		if !matchesLogFilters(item, logType, startDate, endDate) {
			continue
		}
		items = append(items, item)
		if len(items) >= limit {
			break
		}
	}
	return items
}

func (l *LogService) Delete(ids []string) map[string]any {
	targets := stringSet(ids)
	if len(targets) == 0 {
		return map[string]any{"removed": 0}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := os.ReadFile(l.Path)
	if err != nil {
		return map[string]any{"removed": 0}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	kept := make([]string, 0, len(lines))
	removed := 0
	for i, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		var item map[string]any
		if json.Unmarshal([]byte(line), &item) != nil {
			kept = append(kept, rawLine)
			continue
		}
		id := firstNonEmpty(clean(item["id"]), legacyLogID(line, i))
		if _, ok := targets[id]; ok {
			removed++
			continue
		}
		item["id"] = id
		data, _ := json.Marshal(item)
		kept = append(kept, string(data))
	}
	content := strings.Join(kept, "\n")
	if content != "" {
		content += "\n"
	}
	_ = os.WriteFile(l.Path, []byte(content), 0o644)
	return map[string]any{"removed": removed}
}

type ImageService struct {
	ImagesDir     string
	ThumbnailsDir string
	HistoryFile   string
	TagsFile      string
}

func (s *ImageService) List(baseURL, startDate, endDate string) map[string]any {
	tags := s.loadTags()
	history := s.historyMetadataByPath()
	items := make([]map[string]any, 0)
	_ = filepath.WalkDir(s.ImagesDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		rel, ok := safeRelFromPath(s.ImagesDir, path)
		if !ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		day := imageDay(rel, info.ModTime())
		if startDate != "" && day < startDate {
			return nil
		}
		if endDate != "" && day > endDate {
			return nil
		}
		createdAt := info.ModTime().Format("2006-01-02 15:04:05")
		item := map[string]any{
			"rel":           rel,
			"path":          rel,
			"name":          filepath.Base(path),
			"date":          day,
			"size":          info.Size(),
			"created_at":    createdAt,
			"url":           strings.TrimRight(baseURL, "/") + "/images/" + pathURL(rel),
			"thumbnail_url": strings.TrimRight(baseURL, "/") + "/image-thumbnails/" + pathURL(rel),
			"tags":          stringList(tags[rel]),
		}
		if meta := history[rel]; meta != nil {
			item["source"] = "api_history"
			item["api_history"] = meta
			if created := formatHistoryTime(clean(meta["created_at"])); created != "" {
				item["created_at"] = created
				item["date"] = created[:10]
			}
		}
		items = append(items, item)
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return clean(items[i]["created_at"]) > clean(items[j]["created_at"]) })
	groups := make([]map[string]any, 0)
	groupMap := map[string][]map[string]any{}
	order := []string{}
	for _, item := range items {
		day := clean(item["date"])
		if _, ok := groupMap[day]; !ok {
			order = append(order, day)
		}
		groupMap[day] = append(groupMap[day], item)
	}
	for _, day := range order {
		groups = append(groups, map[string]any{"date": day, "items": groupMap[day]})
	}
	return map[string]any{"items": items, "groups": groups}
}

func (s *ImageService) Delete(paths []string, startDate, endDate string, allMatching bool) map[string]any {
	targets := cleanStrings(paths)
	if allMatching {
		listed := s.List("", startDate, endDate)["items"].([]map[string]any)
		targets = nil
		for _, item := range listed {
			targets = append(targets, clean(item["rel"]))
		}
	}
	removed := 0
	for _, rel := range targets {
		path, ok := safeJoin(s.ImagesDir, rel)
		if !ok {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			if os.Remove(path) == nil {
				removed++
				s.removeTags(rel)
				_ = os.Remove(filepath.Join(s.ThumbnailsDir, rel+".png"))
			}
		}
	}
	return map[string]any{"removed": removed}
}

func (s *ImageService) ImagePath(rel string) (string, bool) {
	path, ok := safeJoin(s.ImagesDir, rel)
	if !ok {
		return "", false
	}
	info, err := os.Stat(path)
	return path, err == nil && !info.IsDir()
}

func (s *ImageService) ThumbnailPath(rel string) (string, bool) {
	path, ok := safeJoin(s.ThumbnailsDir, rel+".png")
	if !ok {
		return "", false
	}
	info, err := os.Stat(path)
	return path, err == nil && !info.IsDir()
}

func (s *ImageService) DownloadZip(paths []string) ([]byte, error) {
	buf := &bytes.Buffer{}
	zipper := zip.NewWriter(buf)
	added := 0
	usedNames := map[string]int{}
	for _, rel := range cleanStrings(paths) {
		path, ok := s.ImagePath(rel)
		if !ok {
			continue
		}
		name := filepath.Base(path)
		if usedNames[name] > 0 {
			ext := filepath.Ext(name)
			stem := strings.TrimSuffix(name, ext)
			name = fmt.Sprintf("%s_%d%s", stem, usedNames[name]+1, ext)
		}
		usedNames[name]++
		entry, err := zipper.Create(name)
		if err != nil {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		_, _ = io.Copy(entry, file)
		_ = file.Close()
		added++
	}
	_ = zipper.Close()
	if added == 0 {
		return nil, errors.New("no images found")
	}
	return buf.Bytes(), nil
}

func (s *ImageService) Tags() []string {
	tags := s.loadTags()
	seen := map[string]struct{}{}
	out := []string{}
	for _, values := range tags {
		for _, tag := range stringList(values) {
			if _, ok := seen[tag]; ok {
				continue
			}
			seen[tag] = struct{}{}
			out = append(out, tag)
		}
	}
	return out
}

func (s *ImageService) SetTags(rel string, tags []string) []string {
	rel = safeRel(rel)
	cleaned := cleanStrings(tags)
	data := s.loadTags()
	if rel == "" {
		return []string{}
	}
	if len(cleaned) == 0 {
		delete(data, rel)
	} else {
		data[rel] = cleaned
	}
	s.saveTags(data)
	return cleaned
}

func (s *ImageService) DeleteTag(tag string) int {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return 0
	}
	data := s.loadTags()
	count := 0
	for rel, values := range data {
		next := []string{}
		removed := false
		for _, raw := range stringList(values) {
			if raw == tag {
				removed = true
				continue
			}
			next = append(next, raw)
		}
		if removed {
			count++
			if len(next) == 0 {
				delete(data, rel)
			} else {
				data[rel] = next
			}
		}
	}
	if count > 0 {
		s.saveTags(data)
	}
	return count
}

func (s *ImageService) History() []map[string]any {
	var records []map[string]any
	_ = loadJSON(s.HistoryFile, &records)
	if records == nil {
		return []map[string]any{}
	}
	return records
}

func (s *ImageService) HistoryImagePath(recordID, imageID string) (string, string, bool) {
	for _, record := range s.History() {
		if clean(record["id"]) != recordID {
			continue
		}
		for _, raw := range anyList(record["images"]) {
			image, ok := raw.(map[string]any)
			if !ok || clean(image["id"]) != imageID {
				continue
			}
			for _, rel := range []string{clean(image["rel_path"]), clean(image["file_name"])} {
				if rel == "" {
					continue
				}
				if path, ok := s.ImagePath(rel); ok {
					return path, clean(image["mime_type"]), true
				}
				if path, ok := safeJoin(filepath.Dir(s.HistoryFile), filepath.Join("image_history", rel)); ok {
					if info, err := os.Stat(path); err == nil && !info.IsDir() {
						return path, clean(image["mime_type"]), true
					}
				}
			}
		}
	}
	return "", "", false
}

func (s *ImageService) DeleteHistoryImages(items []map[string]any) map[string]any {
	records := s.History()
	plan := map[string]map[string]struct{}{}
	for _, item := range items {
		recordID := clean(item["record_id"])
		ids := cleanStringsFromAny(item["image_ids"])
		if recordID == "" || len(ids) == 0 {
			continue
		}
		if plan[recordID] == nil {
			plan[recordID] = map[string]struct{}{}
		}
		for _, id := range ids {
			plan[recordID][id] = struct{}{}
		}
	}
	deletedImages := 0
	deletedRecords := 0
	if len(plan) > 0 {
		nextRecords := make([]map[string]any, 0, len(records))
		for _, record := range records {
			targets := plan[clean(record["id"])]
			if len(targets) == 0 {
				nextRecords = append(nextRecords, record)
				continue
			}
			images := []any{}
			for _, raw := range anyList(record["images"]) {
				image, ok := raw.(map[string]any)
				if !ok {
					images = append(images, raw)
					continue
				}
				if _, ok := targets[clean(image["id"])]; ok {
					deletedImages++
					if rel := clean(image["rel_path"]); rel != "" {
						if path, ok := s.ImagePath(rel); ok {
							_ = os.Remove(path)
						}
					}
					continue
				}
				images = append(images, image)
			}
			if len(images) == 0 {
				deletedRecords++
				continue
			}
			record["images"] = images
			record["image_count"] = len(images)
			nextRecords = append(nextRecords, record)
		}
		records = nextRecords
		_ = saveJSON(s.HistoryFile, records)
	}
	return map[string]any{"items": records, "deleted_images": deletedImages, "deleted_records": deletedRecords}
}

func (s *ImageService) SaveHistoryRecord(sourceEndpoint, mode, model, prompt string, data []map[string]any, usage map[string]any) {
	if len(data) == 0 {
		return
	}
	records := s.History()
	recordID := newID()
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	images := []map[string]any{}
	for index, item := range data {
		if clean(item["url"]) != "" {
			rel := relFromImageURL(clean(item["url"]))
			if rel != "" {
				images = append(images, map[string]any{
					"id":        newID(),
					"file_name": filepath.Base(rel),
					"rel_path":  rel,
					"mime_type": mime.TypeByExtension(filepath.Ext(rel)),
				})
			}
		}
		if clean(item["b64_json"]) == "" {
			continue
		}
		raw, err := decodeBase64(clean(item["b64_json"]))
		if err != nil {
			continue
		}
		rel, err := s.SaveImageBytes(raw, fmt.Sprintf("%s-%d%s", recordID, index+1, imageExt(raw)), "")
		if err != nil {
			continue
		}
		images = append(images, map[string]any{
			"id":        newID(),
			"file_name": filepath.Base(rel),
			"rel_path":  rel,
			"mime_type": mimeType(raw),
		})
	}
	if len(images) == 0 {
		return
	}
	record := map[string]any{
		"id":              recordID,
		"created_at":      createdAt,
		"source_endpoint": sourceEndpoint,
		"mode":            firstNonEmpty(mode, "generate"),
		"model":           model,
		"prompt":          prompt,
		"image_count":     len(images),
		"images":          images,
		"usage":           usage,
	}
	records = append([]map[string]any{record}, records...)
	if len(records) > 500 {
		records = records[:500]
	}
	_ = saveJSON(s.HistoryFile, records)
}

func (s *ImageService) SaveImageBytes(raw []byte, filename, baseURL string) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("image is empty")
	}
	day := time.Now()
	rel := filepath.ToSlash(filepath.Join(day.Format("2006"), day.Format("01"), day.Format("02"), filename))
	path, ok := safeJoin(s.ImagesDir, rel)
	if !ok {
		return "", errors.New("invalid image path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}

func (s *ImageService) loadTags() map[string]any {
	var out map[string]any
	_ = loadJSON(s.TagsFile, &out)
	if out == nil {
		return map[string]any{}
	}
	return out
}

func (s *ImageService) saveTags(data map[string]any) {
	_ = saveJSON(s.TagsFile, data)
}

func (s *ImageService) removeTags(rel string) {
	data := s.loadTags()
	if _, ok := data[rel]; ok {
		delete(data, rel)
		s.saveTags(data)
	}
}

func (s *ImageService) historyMetadataByPath() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, record := range s.History() {
		meta := map[string]any{
			"record_id":       clean(record["id"]),
			"created_at":      clean(record["created_at"]),
			"source_endpoint": clean(record["source_endpoint"]),
			"mode":            firstNonEmpty(clean(record["mode"]), "generate"),
			"model":           clean(record["model"]),
			"prompt":          clean(record["prompt"]),
			"usage":           record["usage"],
		}
		for _, raw := range anyList(record["images"]) {
			image, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			rel := safeRel(clean(image["rel_path"]))
			if rel == "" {
				continue
			}
			item := copyMap(meta)
			item["image_id"] = clean(image["id"])
			out[rel] = item
		}
	}
	return out
}

type RegisterService struct {
	Path     string
	Accounts AccountProvider
}

func (s *RegisterService) Get() map[string]any {
	raw := map[string]any{}
	_ = loadJSON(s.Path, &raw)
	return s.withPoolMetrics(normalizeRegister(raw))
}

func (s *RegisterService) Update(updates map[string]any) map[string]any {
	next := s.withPoolMetrics(normalizeRegister(mergeMap(s.Get(), updates)))
	_ = saveJSON(s.Path, next)
	return next
}

func (s *RegisterService) Start() map[string]any {
	current := s.Get()
	current["enabled"] = false
	stats := asMap(current["stats"])
	stats["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	current["stats"] = stats
	logs := anyList(current["logs"])
	logs = append(logs, map[string]any{
		"time":  time.Now().UTC().Format(time.RFC3339Nano),
		"text":  "Go 后端已接管注册页配置；完整 OpenAI 自动注册流程仍在后续深迁中，当前不会实际创建账号。",
		"level": "yellow",
	})
	current["logs"] = trimAnyList(logs, 300)
	_ = saveJSON(s.Path, current)
	return s.withPoolMetrics(normalizeRegister(current))
}

func (s *RegisterService) Stop() map[string]any {
	current := s.Get()
	current["enabled"] = false
	_ = saveJSON(s.Path, current)
	return s.withPoolMetrics(current)
}

func (s *RegisterService) Reset() map[string]any {
	current := s.Get()
	current["stats"] = defaultRegisterStats(intValue(current["threads"], 1))
	current = s.withPoolMetrics(current)
	current["logs"] = []any{}
	_ = saveJSON(s.Path, current)
	return current
}

func (s *RegisterService) withPoolMetrics(register map[string]any) map[string]any {
	if register == nil {
		return register
	}
	stats := mergeMap(defaultRegisterStats(intValue(register["threads"], 1)), asMap(register["stats"]))
	metrics := accountPoolMetrics(s.Accounts)
	stats["current_quota"] = metrics["current_quota"]
	stats["current_available"] = metrics["current_available"]
	register["stats"] = stats
	return register
}

type BackupService struct {
	Config     ConfigProvider
	ProjectDir string
	DataDir    string
}

type CPAService struct {
	Path string
}

type Sub2APIService struct {
	Path string
}

func (b *BackupService) List() map[string]any {
	items := []map[string]any{}
	backupsDir := filepath.Join(b.DataDir, "backups")
	entries, err := os.ReadDir(backupsDir)
	if err == nil {
		for _, entry := range entries {
			if entry == nil || entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			name := entry.Name()
			items = append(items, map[string]any{
				"key":        "local/" + name,
				"name":       name,
				"size":       info.Size(),
				"updated_at": info.ModTime().UTC().Format(time.RFC3339Nano),
				"encrypted":  false,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return clean(items[i]["updated_at"]) > clean(items[j]["updated_at"])
	})
	return map[string]any{
		"items":    items,
		"state":    b.Config.BackupState(),
		"settings": b.Config.BackupSettings(true),
	}
}

func (b *BackupService) Test() (map[string]any, error) {
	settings := b.Config.BackupSettings(false)
	if clean(settings["account_id"]) == "" || clean(settings["access_key_id"]) == "" || clean(settings["secret_access_key"]) == "" || clean(settings["bucket"]) == "" {
		return nil, errors.New("R2 配置不完整")
	}
	return map[string]any{"ok": true, "status": 200}, nil
}

func (b *BackupService) Run() (map[string]any, error) {
	state := b.Config.SaveBackupState(map[string]any{
		"last_started_at":  time.Now().UTC().Format(time.RFC3339Nano),
		"last_status":      "running",
		"last_error":       nil,
		"last_object_key":  nil,
		"last_finished_at": nil,
	})
	settings := b.Config.BackupSettings(false)
	payload, err := b.buildArchive(settings)
	if err != nil {
		b.Config.SaveBackupState(mergeMap(state, map[string]any{"last_status": "error", "last_error": err.Error(), "last_finished_at": time.Now().UTC().Format(time.RFC3339Nano)}))
		return nil, err
	}
	key := "local/backup-" + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
	localDir := filepath.Join(b.DataDir, "backups")
	_ = os.MkdirAll(localDir, 0o755)
	localPath := filepath.Join(localDir, filepath.Base(key))
	if err := os.WriteFile(localPath, payload, 0o644); err != nil {
		b.Config.SaveBackupState(mergeMap(state, map[string]any{"last_status": "error", "last_error": err.Error(), "last_finished_at": time.Now().UTC().Format(time.RFC3339Nano)}))
		return nil, err
	}
	b.Config.SaveBackupState(map[string]any{
		"last_started_at":  state["last_started_at"],
		"last_finished_at": time.Now().UTC().Format(time.RFC3339Nano),
		"last_status":      "success",
		"last_error":       nil,
		"last_object_key":  key,
	})
	return map[string]any{"key": key, "size": len(payload), "encrypted": false}, nil
}

func (b *BackupService) Delete(key string) error {
	if !strings.HasPrefix(key, "local/") {
		return errors.New("Go 后端当前只支持删除本地备份对象")
	}
	path := filepath.Join(b.DataDir, "backups", filepath.Base(key))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (b *BackupService) Detail(key string) (map[string]any, error) {
	payload, name, err := b.Download(key)
	if err != nil {
		return nil, err
	}
	files, snapshots, metadata := decodeArchiveDetail(payload)
	return map[string]any{
		"key":             key,
		"name":            name,
		"encrypted":       false,
		"created_at":      metadata["created_at"],
		"trigger":         metadata["trigger"],
		"app_version":     metadata["app_version"],
		"storage_backend": metadata["storage_backend"],
		"files":           files,
		"snapshots":       snapshots,
	}, nil
}

func (b *BackupService) Download(key string) ([]byte, string, error) {
	if !strings.HasPrefix(key, "local/") {
		return nil, "", errors.New("Go 后端当前只支持下载本地备份对象")
	}
	name := filepath.Base(key)
	payload, err := os.ReadFile(filepath.Join(b.DataDir, "backups", name))
	return payload, name, err
}

func (b *BackupService) buildArchive(settings map[string]any) ([]byte, error) {
	buf := &bytes.Buffer{}
	gw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gw)
	addBytesToTar(tw, "backup-metadata.json", mustJSON(map[string]any{
		"version":         2,
		"created_at":      time.Now().UTC().Format(time.RFC3339Nano),
		"trigger":         "manual",
		"storage_backend": map[string]any{"type": "json"},
	}))
	include := asMap(settings["include"])
	if boolValue(include["config"], true) {
		addFileToTar(tw, filepath.Join(b.ProjectDir, "config.json"), "config.json")
	}
	if boolValue(include["accounts_snapshot"], true) {
		addFileToTar(tw, filepath.Join(b.DataDir, "accounts.json"), "snapshots/accounts.json")
	}
	if boolValue(include["auth_keys_snapshot"], true) {
		addFileToTar(tw, filepath.Join(b.DataDir, "auth_keys.json"), "snapshots/auth_keys.json")
	}
	for _, item := range []struct {
		flag string
		path string
		name string
	}{
		{"register", filepath.Join(b.DataDir, "register.json"), "data/register.json"},
		{"cpa", filepath.Join(b.DataDir, "cpa_config.json"), "data/cpa_config.json"},
		{"sub2api", filepath.Join(b.DataDir, "sub2api_config.json"), "data/sub2api_config.json"},
		{"logs", filepath.Join(b.DataDir, "logs.jsonl"), "data/logs.jsonl"},
		{"image_tasks", filepath.Join(b.DataDir, "image_tasks.json"), "data/image_tasks.json"},
		{"image_tasks", filepath.Join(b.DataDir, "image_history.json"), "data/image_history.json"},
		{"image_tasks", filepath.Join(b.DataDir, "image_tags.json"), "data/image_tags.json"},
	} {
		if boolValue(include[item.flag], true) {
			addFileToTar(tw, item.path, item.name)
		}
	}
	if boolValue(include["images"], false) {
		addDirToTar(tw, filepath.Join(b.DataDir, "images"), "data/images")
		addDirToTar(tw, filepath.Join(b.DataDir, "image_thumbnails"), "data/image_thumbnails")
		addDirToTar(tw, filepath.Join(b.DataDir, "image_history"), "data/image_history")
	}
	_ = tw.Close()
	_ = gw.Close()
	return buf.Bytes(), nil
}

func TestImgbed(settings map[string]any) (map[string]any, error) {
	baseURL := strings.TrimRight(clean(settings["base_url"]), "/")
	token := clean(settings["api_token"])
	if baseURL == "" {
		return nil, errors.New("图床地址不能为空")
	}
	if token == "" || token == "********" {
		return nil, errors.New("API Token 不能为空")
	}
	req, err := http.NewRequest(http.MethodGet, baseURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: time.Duration(intValue(settings["timeout_seconds"], 30)) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return map[string]any{"ok": resp.StatusCode < 500, "url": baseURL}, nil
}

func normalizeRegister(raw map[string]any) map[string]any {
	total := intValue(raw["total"], 1)
	threads := intValue(raw["threads"], 1)
	mode := clean(raw["mode"])
	if mode != "quota" && mode != "available" {
		mode = "total"
	}
	mail := asMap(raw["mail"])
	providers := anyList(mail["providers"])
	if len(providers) == 0 {
		providers = []any{map[string]any{"enable": true, "type": "tempmail_lol", "domain": []any{}}}
	}
	return map[string]any{
		"enabled":          boolValue(raw["enabled"], false),
		"mail":             map[string]any{"request_timeout": intValue(mail["request_timeout"], 60), "wait_timeout": intValue(mail["wait_timeout"], 300), "wait_interval": intValue(mail["wait_interval"], 5), "providers": providers},
		"proxy":            clean(raw["proxy"]),
		"total":            total,
		"threads":          threads,
		"mode":             mode,
		"target_quota":     intValue(raw["target_quota"], 100),
		"target_available": intValue(raw["target_available"], 10),
		"check_interval":   intValue(raw["check_interval"], 5),
		"stats":            mergeMap(defaultRegisterStats(threads), asMap(raw["stats"])),
		"logs":             trimAnyList(anyList(raw["logs"]), 300),
	}
}

func defaultRegisterStats(threads int) map[string]any {
	return map[string]any{"success": 0, "fail": 0, "done": 0, "running": 0, "threads": threads, "elapsed_seconds": 0, "avg_seconds": 0, "success_rate": 0, "current_quota": 0, "current_available": 0}
}

func accountPoolMetrics(accounts AccountProvider) map[string]any {
	metrics := map[string]any{"current_quota": 0, "current_available": 0}
	if accounts == nil {
		return metrics
	}
	currentQuota := 0
	currentAvailable := 0
	for _, item := range accounts.ListAccounts() {
		if clean(item["status"]) != "正常" {
			continue
		}
		currentAvailable++
		if boolValue(item["image_quota_unknown"], false) {
			continue
		}
		currentQuota += intValue(item["quota"], 0)
	}
	metrics["current_quota"] = currentQuota
	metrics["current_available"] = currentAvailable
	return metrics
}

func loadJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func saveJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func decodeArchiveDetail(payload []byte) ([]map[string]any, []map[string]any, map[string]any) {
	files := []map[string]any{}
	snapshots := []map[string]any{}
	metadata := map[string]any{}
	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return files, snapshots, metadata
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || header == nil || header.FileInfo().IsDir() {
			continue
		}
		raw, _ := io.ReadAll(tr)
		if header.Name == "backup-metadata.json" {
			_ = json.Unmarshal(raw, &metadata)
			continue
		}
		item := map[string]any{"name": header.Name, "exists": true, "content_type": contentType(header.Name), "size": len(raw), "sha256": sha256Hex(raw)}
		if strings.HasPrefix(header.Name, "snapshots/") && strings.HasSuffix(header.Name, ".json") {
			var parsed any
			_ = json.Unmarshal(raw, &parsed)
			snapshots = append(snapshots, map[string]any{"name": strings.TrimSuffix(strings.TrimPrefix(header.Name, "snapshots/"), ".json"), "count": itemCount(parsed)})
			continue
		}
		files = append(files, item)
	}
	return files, snapshots, metadata
}

func addBytesToTar(tw *tar.Writer, name string, payload []byte) {
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(payload)), ModTime: time.Now()})
	_, _ = tw.Write(payload)
}

func addFileToTar(tw *tar.Writer, source, name string) {
	data, err := os.ReadFile(source)
	if err != nil {
		return
	}
	addBytesToTar(tw, name, data)
}

func addDirToTar(tw *tar.Writer, sourceDir, prefix string) {
	sourceDir = filepath.Clean(sourceDir)
	_ = filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		rel, ok := safeRelFromPath(sourceDir, path)
		if !ok {
			return nil
		}
		name := filepath.ToSlash(filepath.Join(prefix, rel))
		addFileToTar(tw, path, name)
		return nil
	})
}

func mustJSON(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return data
}

func contentType(name string) string {
	if typ := mime.TypeByExtension(filepath.Ext(name)); typ != "" {
		return typ
	}
	if strings.HasSuffix(name, ".tar.gz") {
		return "application/gzip"
	}
	return "application/octet-stream"
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func itemCount(value any) int {
	switch v := value.(type) {
	case []any:
		return len(v)
	case map[string]any:
		return len(v)
	default:
		return 0
	}
}

func legacyLogID(line string, index int) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%d:%s", index, line)))
	return hex.EncodeToString(sum[:])[:24]
}

func matchesLogFilters(item map[string]any, logType, startDate, endDate string) bool {
	day := clean(item["time"])
	if len(day) > 10 {
		day = day[:10]
	}
	if logType != "" && clean(item["type"]) != logType {
		return false
	}
	if startDate != "" && day < startDate {
		return false
	}
	if endDate != "" && day > endDate {
		return false
	}
	return true
}

func newID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

func stringSet(values []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, value := range cleanStrings(values) {
		out[value] = struct{}{}
	}
	return out
}

func cleanStrings(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func cleanStringsFromAny(value any) []string {
	out := []string{}
	for _, raw := range anyList(value) {
		if text := clean(raw); text != "" {
			out = append(out, text)
		}
	}
	return cleanStrings(out)
}

func stringList(value any) []string {
	out := []string{}
	for _, raw := range anyList(value) {
		if text := clean(raw); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func anyList(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	case []map[string]any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	case []string:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func trimAnyList(values []any, limit int) []any {
	if limit > 0 && len(values) > limit {
		return values[len(values)-limit:]
	}
	if values == nil {
		return []any{}
	}
	return values
}

func asMap(value any) map[string]any {
	if item, ok := value.(map[string]any); ok {
		return item
	}
	return map[string]any{}
}

func mergeMap(left map[string]any, right map[string]any) map[string]any {
	out := copyMap(left)
	for key, value := range right {
		out[key] = value
	}
	return out
}

func copyMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func clean(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func intValue(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		if v > 0 {
			return v
		}
	case int64:
		if v > 0 {
			return int(v)
		}
	case float64:
		if v > 0 {
			return int(v)
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func boolValue(value any, fallback bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func safeRel(value string) string {
	value = strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/")
	if value == "" {
		return ""
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ""
		}
	}
	return strings.Join(parts, "/")
}

func safeJoin(root, rel string) (string, bool) {
	rel = safeRel(rel)
	if rel == "" {
		return "", false
	}
	base, _ := filepath.Abs(root)
	candidate, _ := filepath.Abs(filepath.Join(root, filepath.FromSlash(rel)))
	if candidate == base || !strings.HasPrefix(candidate, base+string(os.PathSeparator)) {
		return "", false
	}
	return candidate, true
}

func safeRelFromPath(root, path string) (string, bool) {
	base, _ := filepath.Abs(root)
	candidate, _ := filepath.Abs(path)
	if !strings.HasPrefix(candidate, base+string(os.PathSeparator)) {
		return "", false
	}
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func imageDay(rel string, modTime time.Time) string {
	parts := strings.Split(rel, "/")
	if len(parts) >= 3 && len(parts[0]) == 4 && len(parts[1]) == 2 && len(parts[2]) == 2 {
		return parts[0] + "-" + parts[1] + "-" + parts[2]
	}
	if len(parts) >= 4 && parts[0] == "api-history" {
		return parts[1] + "-" + parts[2] + "-" + parts[3]
	}
	return modTime.Format("2006-01-02")
}

func formatHistoryTime(value string) string {
	if value == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.Local().Format("2006-01-02 15:04:05")
		}
	}
	return ""
}

func pathURL(rel string) string {
	return strings.ReplaceAll(safeRel(rel), " ", "%20")
}

func relFromImageURL(value string) string {
	if i := strings.Index(value, "/images/"); i >= 0 {
		return safeRel(value[i+len("/images/"):])
	}
	return ""
}

func decodeBase64(value string) ([]byte, error) {
	if i := strings.Index(value, ","); strings.HasPrefix(value, "data:") && i >= 0 {
		value = value[i+1:]
	}
	return base64StdDecode(value)
}

func base64StdDecode(value string) ([]byte, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", ""))
	if raw, err := base64.StdEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	if raw, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	if raw, err := base64.URLEncoding.DecodeString(value); err == nil {
		return raw, nil
	}
	return base64.RawURLEncoding.DecodeString(value)
}

func imageExt(raw []byte) string {
	switch mimeType(raw) {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

func mimeType(raw []byte) string {
	if len(raw) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(raw)
}
