# Go 后端 Docker 部署说明

本文说明当前仓库的默认 Docker 形态。

现在根目录 `Dockerfile` 和 `docker-compose.yml` 已切到单容器双进程：

- `nginx` 对外提供原来的 Web 静态页
- `chatgpt2api-go` 在容器内提供 API

浏览器直接访问根站点即可，不需要再单独部署一个 Web 容器。


## 启动

在项目根目录执行：

```powershell
$env:CHATGPT2API_AUTH_KEY="你的管理密钥"
docker compose up --build -d
```

根目录 `docker-compose.yml` 不再内置默认管理密钥。
必须通过 `CHATGPT2API_AUTH_KEY` 设置，或把 `config.json` 中的
`auth-key` 改成真实值。

如果需要代理：

```powershell
$env:CHATGPT2API_AUTH_KEY="你的管理密钥"
$env:CHATGPT2API_PROXY="http://host.docker.internal:7890"
docker compose up --build -d
```

本地调试如果只想临时使用测试密钥，可以设成：

```powershell
$env:CHATGPT2API_AUTH_KEY="go-backend-local-key"
```


## 端口

- 页面和 API 都通过 `http://127.0.0.1:3000`
- 容器内 `nginx` 监听 `80`
- Go API 监听容器内 `8001`


## 基础验证

```powershell
$base = "http://127.0.0.1:3000"
$key = "你的管理密钥"
$headers = @{ Authorization = "Bearer $key" }

Invoke-RestMethod "$base/health"
Invoke-RestMethod "$base/api/accounts" -Headers $headers
Invoke-RestMethod "$base/v1/models" -Headers $headers
```


## 账号导入

```powershell
$token = "这里填测试账号 access_token"
$body = @{ tokens = @($token) } | ConvertTo-Json -Depth 4
Invoke-RestMethod "$base/api/accounts" `
  -Method Post `
  -Headers $headers `
  -ContentType "application/json" `
  -Body $body
```


## 真实验证建议

优先确认这几个动作：

1. `POST /api/accounts/refresh`
2. `POST /v1/chat/completions`
3. `POST /v1/images/generations`
4. `POST /v1/images/edits`
5. `GET /api/logs`
6. `GET /api/backups`


## 当前已接入的 Go 能力

- 账号池管理
- 账号刷新
- 设置
- 日志管理
- 图片管理
- 图片历史
- 备份列表与本地备份文件
- 注册页配置
- CPA / Sub2API 连接管理
- OpenAI 兼容 `responses` / `messages`
- OpenAI 兼容图片生成与图片编辑

## 当前边界

- 当前 Go 版存储以本地 JSON 为主，不是 SQLite / PostgreSQL / Git 多后端模式。
- 当前已切到单容器双进程，不需要再拆成两个容器。
- 注册、图床、R2 这些能力已接入接口层，但完整自动化链路还在继续完善。
