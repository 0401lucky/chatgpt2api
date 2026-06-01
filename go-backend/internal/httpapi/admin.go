package httpapi

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"chatgpt2api-go-backend/internal/localdata"
)

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"config": a.config.PublicConfig()})
	case http.MethodPost:
		var body map[string]any
		if !decodeJSONBody(w, r, &body) {
			return
		}
		config, err := a.config.Update(body)
		if err != nil {
			writeDetailError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": config})
	default:
		writeMethodNotAllowed(w)
	}
}

func (a *App) handleStorageInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.config.StorageInfo())
}

func (a *App) handleProxy(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"proxy": proxyPayload(a.config.Proxy)})
	case http.MethodPost:
		var body struct {
			Enabled *bool  `json:"enabled"`
			URL     string `json:"url"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		enabled := strings.TrimSpace(body.URL) != ""
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		proxyURL := strings.TrimSpace(body.URL)
		if !enabled {
			proxyURL = ""
		}
		config, err := a.config.Update(map[string]any{"proxy": proxyURL})
		if err != nil {
			writeDetailError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"proxy": proxyPayload(fmt.Sprint(config["proxy"]))})
	default:
		writeMethodNotAllowed(w)
	}
}

func (a *App) handleProxyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	candidate := strings.TrimSpace(body.URL)
	if candidate == "" {
		candidate = a.config.Proxy
	}
	if candidate == "" {
		writeDetailError(w, http.StatusBadRequest, "proxy url is required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": testProxyURL(r.Context(), candidate)})
}

func (a *App) handleUserKeys(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"items": a.auth.ListKeys("user")})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		item, key, err := a.auth.CreateKey("user", body.Name)
		if err != nil {
			writeDetailError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item, "key": key, "items": a.auth.ListKeys("user")})
	default:
		writeMethodNotAllowed(w)
	}
}

func (a *App) handleUserKeyItem(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/auth/users/"), "/")
	if id == "" {
		writeDetailError(w, http.StatusNotFound, "这条用户密钥不存在，可能已经被删除")
		return
	}
	switch r.Method {
	case http.MethodPost:
		var body map[string]any
		if !decodeJSONBody(w, r, &body) {
			return
		}
		if len(body) == 0 {
			writeDetailError(w, http.StatusBadRequest, "还没有检测到改动，请修改后再保存")
			return
		}
		item, err := a.auth.UpdateKey(id, body, "user")
		if err != nil {
			writeDetailError(w, http.StatusBadRequest, err.Error())
			return
		}
		if item == nil {
			writeDetailError(w, http.StatusNotFound, "这条用户密钥不存在，可能已经被删除")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item, "items": a.auth.ListKeys("user")})
	case http.MethodDelete:
		removed, err := a.auth.DeleteKey(id, "user")
		if err != nil {
			writeDetailError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !removed {
			writeDetailError(w, http.StatusNotFound, "这条用户密钥不存在，可能已经被删除")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": a.auth.ListKeys("user")})
	default:
		writeMethodNotAllowed(w)
	}
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, map[string]any{
		"items": a.local.Logs().List(strings.TrimSpace(q.Get("type")), strings.TrimSpace(q.Get("start_date")), strings.TrimSpace(q.Get("end_date")), 200),
	})
}

func (a *App) handleLogsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusOK, a.local.Logs().Delete(body.IDs))
}

func (a *App) handleImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	q := r.URL.Query()
	writeJSON(w, http.StatusOK, a.local.Images().List(resolveBaseURL(a.config.BaseURL, r), q.Get("start_date"), q.Get("end_date")))
}

func (a *App) handleImagesDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var body struct {
		Paths       []string `json:"paths"`
		StartDate   string   `json:"start_date"`
		EndDate     string   `json:"end_date"`
		AllMatching bool     `json:"all_matching"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusOK, a.local.Images().Delete(body.Paths, body.StartDate, body.EndDate, body.AllMatching))
}

func (a *App) handleImagesDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	payload, err := a.local.Images().DownloadZip(body.Paths)
	if err != nil {
		writeDetailError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="images.zip"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) handleImageDownloadSingle(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/api/images/download/")
	path, ok := a.local.Images().ImagePath(rel)
	if !ok {
		writeDetailError(w, http.StatusNotFound, "image not found")
		return
	}
	http.ServeFile(w, r, path)
}

func (a *App) handleImageTags(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"tags": a.local.Images().Tags()})
	case http.MethodPost:
		var body struct {
			Path string   `json:"path"`
			Tags []string `json:"tags"`
		}
		if !decodeJSONBody(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Path) == "" {
			writeDetailError(w, http.StatusBadRequest, "path is required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tags": a.local.Images().SetTags(body.Path, body.Tags)})
	default:
		writeMethodNotAllowed(w)
	}
}

func (a *App) handleImageTagItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	tag, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/images/tags/"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed_from": a.local.Images().DeleteTag(tag)})
}

func (a *App) handleStaticImage(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/images/")
	path, ok := a.local.Images().ImagePath(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (a *App) handleStaticImageThumbnail(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/image-thumbnails/")
	path, ok := a.local.Images().ThumbnailPath(rel)
	if !ok {
		path, ok = a.local.Images().ImagePath(rel)
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (a *App) handleImageHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": a.local.Images().History()})
}

func (a *App) handleImageHistoryImage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/image-history/"), "/"), "/")
	if len(parts) != 3 || parts[1] != "images" {
		writeDetailError(w, http.StatusNotFound, "image not found")
		return
	}
	path, mimeType, ok := a.local.Images().HistoryImagePath(parts[0], parts[2])
	if !ok {
		writeDetailError(w, http.StatusNotFound, "image not found")
		return
	}
	if mimeType != "" {
		w.Header().Set("Content-Type", mimeType)
	}
	http.ServeFile(w, r, path)
}

func (a *App) handleImageHistoryDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusOK, a.local.Images().DeleteHistoryImages(body.Items))
}

func (a *App) handleBackups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.local.Backup().List())
}

func (a *App) handleBackupsRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	result, err := a.local.Backup().Run()
	if err != nil {
		a.logEvent("backup", "手动备份失败", map[string]any{"error": err.Error()})
		writeDetailError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.logEvent("backup", "手动备份成功", map[string]any{"key": result["key"], "size": result["size"], "encrypted": result["encrypted"]})
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func (a *App) handleBackupsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if err := a.local.Backup().Delete(body.Key); err != nil {
		writeDetailError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.logEvent("backup", "删除备份", map[string]any{"key": body.Key})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleBackupsDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	item, err := a.local.Backup().Detail(r.URL.Query().Get("key"))
	if err != nil {
		writeDetailError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (a *App) handleBackupsDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	payload, name, err := a.local.Backup().Download(r.URL.Query().Get("key"))
	if err != nil {
		writeDetailError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(name)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) handleBackupTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	result, err := a.local.Backup().Test()
	if err != nil {
		writeDetailError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func (a *App) handleImgbedTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	var body map[string]any
	if !decodeJSONBody(w, r, &body) {
		return
	}
	saved := a.config.ImgbedSettings(false)
	for key, value := range body {
		if strings.TrimSpace(fmt.Sprint(value)) != "" && strings.TrimSpace(fmt.Sprint(value)) != "********" {
			saved[key] = value
		}
	}
	result, err := localdata.TestImgbed(saved)
	if err != nil {
		writeDetailError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"register": a.local.Register().Get()})
	case http.MethodPost:
		var body map[string]any
		if !decodeJSONBody(w, r, &body) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"register": a.local.Register().Update(body)})
	default:
		writeMethodNotAllowed(w)
	}
}

func (a *App) handleRegisterStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"register": a.local.Register().Start()})
}

func (a *App) handleRegisterStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"register": a.local.Register().Stop()})
}

func (a *App) handleRegisterReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"register": a.local.Register().Reset()})
}

func (a *App) handleRegisterEvents(w http.ResponseWriter, r *http.Request) {
	identity := a.auth.Authenticate(r.URL.Query().Get("token"))
	if identity == nil || identity.Role != "admin" {
		writeDetailError(w, http.StatusUnauthorized, "密钥无效或已失效，请重新登录")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	payload, _ := json.Marshal(a.local.Register().Get())
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	if flusher != nil {
		flusher.Flush()
	}
}

func (a *App) handleCPAPools(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	cpa := a.local.CPA()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"pools": cpa.ListPools()})
	case http.MethodPost:
		var body map[string]any
		if !decodeJSONBody(w, r, &body) {
			return
		}
		pool, err := cpa.AddPool(body)
		if err != nil {
			writeDetailError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"pool": pool, "pools": cpa.ListPools()})
	default:
		writeMethodNotAllowed(w)
	}
}

func (a *App) handleCPAPoolItem(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	cpa := a.local.CPA()
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/cpa/pools/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeDetailError(w, http.StatusNotFound, "pool not found")
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "files" && r.Method == http.MethodGet {
		files, err := cpa.ListRemoteFiles(r.Context(), id)
		if err != nil {
			writeDetailError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"pool_id": id, "files": files})
		return
	}
	if len(parts) == 2 && parts[1] == "import" {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"import_job": cpa.ImportJob(id)})
			return
		}
		if r.Method == http.MethodPost {
			var body struct {
				Names []string `json:"names"`
			}
			if !decodeJSONBody(w, r, &body) {
				return
			}
			job, err := cpa.StartImport(r.Context(), id, body.Names, a.accounts)
			if err != nil {
				writeDetailError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"import_job": job})
			return
		}
	}
	switch r.Method {
	case http.MethodPost:
		var body map[string]any
		if !decodeJSONBody(w, r, &body) {
			return
		}
		pool, err := cpa.UpdatePool(id, body)
		if err != nil {
			writeDetailError(w, http.StatusBadRequest, err.Error())
			return
		}
		if pool == nil {
			writeDetailError(w, http.StatusNotFound, "pool not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"pool": pool, "pools": cpa.ListPools()})
	case http.MethodDelete:
		if !cpa.DeletePool(id) {
			writeDetailError(w, http.StatusNotFound, "pool not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"pools": cpa.ListPools()})
	default:
		writeMethodNotAllowed(w)
	}
}

func (a *App) handleSub2APIServers(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	service := a.local.Sub2API()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"servers": service.ListServers()})
	case http.MethodPost:
		var body map[string]any
		if !decodeJSONBody(w, r, &body) {
			return
		}
		server, err := service.AddServer(body)
		if err != nil {
			writeDetailError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"server": server, "servers": service.ListServers()})
	default:
		writeMethodNotAllowed(w)
	}
}

func (a *App) handleSub2APIServerItem(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireAdmin(w, r); !ok {
		return
	}
	service := a.local.Sub2API()
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sub2api/servers/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeDetailError(w, http.StatusNotFound, "server not found")
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "groups" && r.Method == http.MethodGet {
		groups, err := service.ListRemoteGroups(r.Context(), id)
		if err != nil {
			writeDetailError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"server_id": id, "groups": groups})
		return
	}
	if len(parts) == 2 && parts[1] == "accounts" && r.Method == http.MethodGet {
		accounts, err := service.ListRemoteAccounts(r.Context(), id)
		if err != nil {
			writeDetailError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"server_id": id, "accounts": accounts})
		return
	}
	if len(parts) == 2 && parts[1] == "import" {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"import_job": service.ImportJob(id)})
			return
		}
		if r.Method == http.MethodPost {
			var body struct {
				AccountIDs []string `json:"account_ids"`
			}
			if !decodeJSONBody(w, r, &body) {
				return
			}
			job, err := service.StartImport(r.Context(), id, body.AccountIDs, a.accounts)
			if err != nil {
				writeDetailError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"import_job": job})
			return
		}
	}
	switch r.Method {
	case http.MethodPost:
		var body map[string]any
		if !decodeJSONBody(w, r, &body) {
			return
		}
		server, err := service.UpdateServer(id, body)
		if err != nil {
			writeDetailError(w, http.StatusBadRequest, err.Error())
			return
		}
		if server == nil {
			writeDetailError(w, http.StatusNotFound, "server not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"server": server, "servers": service.ListServers()})
	case http.MethodDelete:
		if !service.DeleteServer(id) {
			writeDetailError(w, http.StatusNotFound, "server not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"servers": service.ListServers()})
	default:
		writeMethodNotAllowed(w)
	}
}

func proxyPayload(proxyURL string) map[string]any {
	proxyURL = strings.TrimSpace(proxyURL)
	return map[string]any{"enabled": proxyURL != "", "url": proxyURL}
}

func testProxyURL(ctx context.Context, proxyURL string) map[string]any {
	started := time.Now()
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return map[string]any{"ok": false, "status": 0, "latency_ms": 0, "error": "invalid proxy url"}
	}
	transport := &http.Transport{Proxy: http.ProxyURL(parsed)}
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://chatgpt.com/api/auth/csrf", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (chatgpt2api proxy test)")
	resp, err := client.Do(req)
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return map[string]any{"ok": false, "status": 0, "latency_ms": latency, "error": err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	ok := resp.StatusCode < 500
	var errorText any
	if !ok {
		errorText = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return map[string]any{"ok": ok, "status": resp.StatusCode, "latency_ms": latency, "error": errorText}
}

func resolveBaseURL(configured string, r *http.Request) string {
	if configured = strings.TrimRight(strings.TrimSpace(configured), "/"); configured != "" {
		return configured
	}
	proto := firstHeader(r, "x-forwarded-proto")
	if proto == "" {
		proto = "http"
		if r.TLS != nil {
			proto = "https"
		}
	}
	host := firstHeader(r, "x-forwarded-host")
	if host == "" {
		host = r.Host
	}
	return proto + "://" + host
}

func firstHeader(r *http.Request, name string) string {
	value := r.Header.Get(name)
	if before, _, ok := strings.Cut(value, ","); ok {
		return strings.TrimSpace(before)
	}
	return strings.TrimSpace(value)
}

// 确保 archive/zip 仍被 Go 编译器校验到；旧版实现依赖 zip 包构建下载包。
var _ = zip.Deflate
