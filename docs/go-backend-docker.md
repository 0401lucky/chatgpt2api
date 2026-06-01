# Go 后端 Docker Desktop 验证说明

本文用于 Go 后端单容器本地验证。

根目录 `Dockerfile` 已经切换为 Go 后端镜像。
Zeabur 这类默认读取根目录 `Dockerfile` 的平台，重新部署后会直接使用
Go 后端，不需要单独切换到 `go-backend` 目录。

当前形态不是两个容器：

- 对外只有一个容器：`chatgpt2api-go-backend`
- 对外只有一个入口：`http://127.0.0.1:8001`
- 容器内有两个进程：
  - `nginx`：监听容器 `80` 端口，服务原来的 Web 静态页面
  - `chatgpt2api-go`：监听容器本地 `8001` 端口，提供 Go 后端 API

请求流向：

```text
浏览器 / OpenAI 客户端
        ↓
http://127.0.0.1:8001
        ↓
容器内 nginx:80
        ├── 静态页面：/, /login, /accounts ...
        └── API 反代：/auth/*, /api/*, /v1/* -> Go:8001
```

因此浏览器直接打开 `http://127.0.0.1:8001/` 应该看到原来的网页，
不需要再额外部署一个 Web 容器。


## 启动

在项目根目录执行：

```powershell
docker compose -f docker-compose.go.yml up --build -d
```

默认管理密钥是：

```text
go-backend-local-key
```

如果要改密钥，启动前设置：

```powershell
$env:CHATGPT2API_AUTH_KEY="你的管理密钥"
docker compose -f docker-compose.go.yml up --build -d
```

如果访问 ChatGPT 需要代理，启动前设置：

```powershell
$env:CHATGPT2API_PROXY="http://host.docker.internal:7890"
docker compose -f docker-compose.go.yml up --build -d
```

容器内会挂载当前项目：

- `./config.json` 到 `/app/config.json`
- `./VERSION` 到 `/app/VERSION`
- `./data` 到 `/app/data`


## 页面验证

打开浏览器访问：

```text
http://127.0.0.1:8001/
```

登录密钥默认是：

```text
go-backend-local-key
```

登录后当前优先验证 `号池管理` 页面。
这部分已经接入 Go 后端的核心账号接口。

当前 Go 后端已迁移的 Web/API 能力：

- `POST /auth/login`
- `GET /api/accounts`
- `POST /api/accounts`
- `DELETE /api/accounts`
- `POST /api/accounts/update`
- `POST /api/accounts/refresh`
- `GET /api/image-tasks`
- `POST /api/image-tasks/generations`
- `GET /api/creation-tasks`
- `POST /api/creation-tasks/image-generations`
- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/images/generations`

其中 `POST /v1/images/generations` 已支持：

- `response_format=url`
- `response_format=b64_json`

图片生成链路已接入 Turnstile token 解析和请求头传递。
如果上游挑战格式再次变化，Go 后端会返回明确错误，避免静默失败。
异步图片任务现在会优先保存 `b64_json`，页面展示不再依赖
ChatGPT 官方临时图片地址。

还没有迁移完的页面能力：

- 图片编辑 / 图生图：
  - `POST /api/image-tasks/edits`
  - `POST /api/creation-tasks/image-edits`
  - `POST /v1/images/edits`
- 设置页、日志页、注册机、备份、图床、CPA、Sub2API 等周边 API

这些页面可以作为后续 Go 化范围继续迁移。


## 基础验证

```powershell
$base = "http://127.0.0.1:8001"
$key = "go-backend-local-key"
$headers = @{ Authorization = "Bearer $key" }

Invoke-RestMethod "$base/health"
Invoke-RestMethod "$base/v1/models" -Headers $headers
```

根页面也可以用命令验证：

```powershell
Invoke-WebRequest "$base/" -UseBasicParsing
```

预期返回 `200`，内容中包含前端 HTML。


## 导入测试账号

不要把真实 token 写进文档或日志。
可以在本机 PowerShell 中临时放入变量，再通过管理接口导入：

```powershell
$token = "这里填测试账号 access_token"
$body = @{ tokens = @($token) } | ConvertTo-Json -Depth 4
Invoke-RestMethod "$base/api/accounts" `
  -Method Post `
  -Headers $headers `
  -ContentType "application/json" `
  -Body $body
```

导入后，Go 后端会写入挂载目录 `./data/accounts.json`。
后续可以继续验证账号刷新与文本接口：

```powershell
Invoke-RestMethod "$base/api/accounts/refresh" `
  -Method Post `
  -Headers $headers `
  -ContentType "application/json" `
  -Body "{}"

$chatBody = @{
  model = "auto"
  messages = @(@{ role = "user"; content = "请只回复 pong" })
} | ConvertTo-Json -Depth 8

Invoke-RestMethod "$base/v1/chat/completions" `
  -Method Post `
  -Headers $headers `
  -ContentType "application/json" `
  -Body $chatBody
```


## 图片生成验证

图片生成需要先导入可用账号。
不要频繁重复运行真实图片生成请求，避免浪费额度或触发上游风控。

异步任务接口：

```powershell
$taskBody = @{
  client_task_id = "local-test-$([guid]::NewGuid().ToString())"
  prompt = "生成一张极简图：白色背景中央一个红色圆点"
  model = "gpt-image-2"
  size = "1024x1024"
} | ConvertTo-Json -Depth 8

$task = Invoke-RestMethod "$base/api/image-tasks/generations" `
  -Method Post `
  -Headers $headers `
  -ContentType "application/json" `
  -Body $taskBody

Start-Sleep -Seconds 15
Invoke-RestMethod "$base/api/image-tasks?ids=$($task.id)" -Headers $headers
```

兼容参考项目的任务别名：

```powershell
Invoke-RestMethod "$base/api/creation-tasks?ids=$($task.id)" -Headers $headers
```

OpenAI 兼容图片接口：

```powershell
$imageBody = @{
  prompt = "生成一张极简图：白色背景中央一个蓝色圆点"
  model = "gpt-image-2"
  size = "1024x1024"
  response_format = "b64_json"
} | ConvertTo-Json -Depth 8

Invoke-RestMethod "$base/v1/images/generations" `
  -Method Post `
  -Headers $headers `
  -ContentType "application/json" `
  -Body $imageBody
```

图生图接口本阶段会返回明确的 `501`，不是静默失败。


## 停止

```powershell
docker compose -f docker-compose.go.yml down
```


## 本轮本机验证记录

已完成：

- `cd web && npm run build` 通过，静态页面包含 `/`、`/login`、`/accounts` 等路由。
- `cd go-backend && go test ./...` 通过。
- `docker compose -f docker-compose.go.yml config` 通过，确认外部 `8001`
  映射到容器 `80`。
- Docker Desktop 重启后，`docker compose -f docker-compose.go.yml up --build -d --force-recreate`
  构建并启动成功。
- 容器状态为 `healthy`，端口映射为 `0.0.0.0:8001->80/tcp`。
- `GET /` 返回 `200`，内容是原 Next 静态页面，不再是 `404 page not found`。
- `GET /login/` 返回 `200`。
- Playwright 实测浏览器打开 `/` 后跳转到 `/login/`，使用默认密钥登录后进入 `/accounts/`。
- `POST /auth/login` 返回管理员身份。
- `GET /api/accounts` 返回成功，当前账号池有 1 个 Plus 账号，图片额度约 118。
- `GET /v1/models` 真实访问上游成功，返回 10 个模型，包含 `auto` 与 `gpt-5`。
- `POST /v1/chat/completions` 在无账号时返回预期的
  `503 no_available_account`，不会误打上游。
- `POST /api/image-tasks/generations` 已用真实账号完成一次文生图任务，
  prompt 为“白色背景中央一个红色圆点”，最终任务状态为 `success`。
- `POST /v1/images/generations` 已用真实账号完成一次
  `response_format=b64_json` 文生图请求，返回 `url` 与 `b64_json`。
- `/api/creation-tasks` 与 `/api/creation-tasks/image-generations`
  已作为任务路由别名接入 Go 后端。

当前仍待迁移：

- 图片编辑 / 图生图。
- 设置页、日志页、注册机、备份、图床、CPA、Sub2API 等周边 API。
