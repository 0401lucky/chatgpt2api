# Go 后端重写计划书

本文基于当前 Python 项目，以及两个外部 Go 实现
`jiujiu532/chatgpt2api-go` 和 `ZyphrZero/chatgpt2api` 的代码研究整理。

结论先说：**可以用 Go 重写后端，但不建议一次性全量替换**。
更稳妥的路线是先做一个旁路 Go 核心服务，优先迁移账号池、上游
ChatGPT Web 协议、OpenAI 兼容核心接口，再用压测和真实账号小流量验证效果。


## 🧾 实施状态

更新时间：2026-06-01

### 当前最新状态

Go 后端试做已经跑到核心可用阶段，且已经做过真实验证：

- 采用单容器双进程形态。
- `nginx` 对外提供原 Web 静态页。
- Go 进程负责 API。
- 原前端源码没有重写。
- `GET /health`、`GET /api/accounts`、`GET /api/image-tasks`、
  `GET /api/creation-tasks`、`POST /api/image-tasks/generations`、
  `POST /api/creation-tasks/image-generations`、`GET /v1/models`、
  `POST /v1/chat/completions`、`POST /v1/images/generations`
  已真实验证可用。
- `POST /v1/images/generations` 已支持 `response_format=url`
  和 `response_format=b64_json`。
- `/api/creation-tasks` 与 `/api/creation-tasks/image-generations`
  已作为兼容别名接入。
- `gpt-image-2` 已映射到 `gpt-5-5`。
- Turnstile 已修，真实 live test 可解出可用 token。
- 图生图 / 图片编辑仍未迁移，相关接口返回明确 `501`。
- `go test ./...` 已通过，Docker Desktop 真实启动也已通过。

### 历史推进记录

下面保留的是逐步推进过程；如果与上面的当前状态冲突，
以“当前最新状态”为准。

已完成第一批 Go 旁路核心切片：

1. 新增 `go-backend` 独立 Go 工程。
2. 使用标准库 `net/http` 实现旁路服务，不影响现有 Python 后端。
3. 兼容读取当前项目的 `config.json`、`VERSION` 和 `data` 目录。
4. 兼容当前鉴权模型：
   - `config.json` / `CHATGPT2API_AUTH_KEY` 管理员密钥。
   - `data/auth_keys.json` 专用用户密钥。
5. 实现基础接口：
   - `GET /health`
   - `GET /version`
   - `POST /auth/login`
   - `GET /api/accounts`
   - `POST /api/accounts`
   - `DELETE /api/accounts`
   - `POST /api/accounts/update`
   - `POST /api/accounts/refresh`
6. 实现本地账号池能力：
   - 读取和保存 `data/accounts.json`。
   - 账号新增、删除、按账号 ID 更新。
   - 公开账号信息自动隐藏完整 `access_token`。
   - 账号 ID 与 token preview 规则兼容 Python 后端。
   - 文本账号本地轮询选择。
   - 图片账号并发槽位预留与释放。
   - 图片成功后扣减额度并在额度归零时标记限流。
7. 账号刷新接口当前只返回明确的“Go 后端账号远程刷新尚未实现”错误，
   不会在未验证 TLS 指纹前请求真实上游。
8. 已添加 Go 单元测试和 HTTP 路由测试。

已验证：

```bash
cd go-backend
go test ./...
```

结果：全部通过。

额外烟测：

- 使用临时 `CHATGPT2API_AUTH_KEY` 启动 Go 服务。
- `GET /health` 返回正常。
- `POST /auth/login` 返回管理员身份。
- 烟测结束后已停止进程。

注意：当前仓库 `config.json` 中的 `auth-key` 仍是占位值。
本地直接启动 Go 服务时，需要先设置 `CHATGPT2API_AUTH_KEY`，
或把 `config.json` 中的 `auth-key` 改成真实管理密钥。

第一批结束时还没做：

- ChatGPT Web 上游请求。
- `surf` / `uTLS` 浏览器指纹客户端。
- PoW / sentinel requirements。
- `/v1/models`。
- `/v1/chat/completions`。
- `/v1/images/generations`。
- 图片下载、保存和图床逻辑。

下一步建议直接进入“阶段 3：上游浏览器指纹客户端”。
这是决定 Go 重写是否真正可用的最大风险点。

已完成第二批 Go 上游验证切片：

1. 引入 `surf` 与 `uTLS` 相关依赖，版本跟外部 Go 项目保持一致。
2. 新增 Go 代理/浏览器指纹客户端：
   - 支持读取 `config.json` 中的 `proxy`。
   - 支持 `CHATGPT2API_PROXY` 环境变量覆盖。
   - 支持按账号读取 `user-agent`、`impersonate`、`oai-device-id`、
     `oai-session-id`、`sec-ch-ua` 等字段。
3. 新增 Go 上游客户端：
   - 首页 bootstrap。
   - 匿名 `/backend-anon/models`。
   - 登录态 `/backend-api/me`。
   - 登录态 `/backend-api/conversation/init`。
4. 接入真实账号刷新路径：
   - `POST /api/accounts/refresh` 现在会调用 Go 上游客户端。
   - 成功后会更新账号邮箱、用户 ID、套餐类型、图片额度、恢复时间和状态。
5. 接入 `GET /v1/models`：
   - 保持本地 Bearer Key 鉴权。
   - 使用匿名 ChatGPT 模型接口。
   - 自动补充本项目本地模型别名。
6. 新增 fake upstream 单元测试：
   - `/v1/models` 解析。
   - 账号刷新解析。
   - HTTP 层鉴权与路由。

已验证：

```bash
cd go-backend
go test ./...
```

结果：全部通过。

真实网络烟测：

- 使用临时 `CHATGPT2API_AUTH_KEY` 启动 Go 服务。
- 调用 `GET /health` 成功。
- 调用 `GET /v1/models` 成功。
- 上游真实返回模型列表，并包含 `auto`、`codex-gpt-image-2`、`gpt-5`、
  `gpt-image-2` 等模型。
- 烟测结束后已停止进程。

未完成的真实验证：

- 账号远程刷新代码已接入，但当前 `data/accounts.json` 为空。
- 本机也没有检测到 `CHATGPT2API_TEST_ACCESS_TOKEN`、
  `CHATGPT2API_ACCESS_TOKEN`、`OPENAI_ACCESS_TOKEN` 或
  `CHATGPT_ACCESS_TOKEN`。
- 因此还没有对真实账号执行 `/backend-api/me` 和
  `/backend-api/conversation/init` 验证。

如果要做账号刷新真实验证，需要补充一个测试账号的 `access_token`。
推荐用临时环境变量 `CHATGPT2API_TEST_ACCESS_TOKEN` 提供，不要写进文档或日志。

已完成第三批 Go 文本核心切片：

1. 新增 Go 侧 sentinel / PoW / Turnstile 基础能力：
   - 首页 bootstrap 后提取 PoW 脚本资源。
   - 生成 legacy requirements token。
   - 支持上游 `proofofwork.required` 时生成 proof token。
   - 支持动态小数 opcode 和二级程序执行的 Turnstile token 求解。
   - Turnstile 求解失败时返回明确错误，不静默伪造通过。
   - Arkose 仍保持明确报错，不静默伪造通过。
2. 新增 Conversation SSE 链路：
   - 登录态 `/backend-api/sentinel/chat-requirements`。
   - 登录态 `/backend-api/conversation`。
   - SSE `data:` payload 逐行读取，并支持请求上下文取消。
3. 新增轻量协议转换层：
   - 标准 `messages` / `prompt` 转 ChatGPT Web conversation messages。
   - 解析 assistant 完整消息与 patch append / replace 事件。
   - 输出 OpenAI 兼容 `chat.completion`。
   - 输出 OpenAI 兼容流式 `chat.completion.chunk`。
4. 接入 `POST /v1/chat/completions`：
   - 复用当前 Bearer Key 鉴权。
   - 从 Go 账号池选择文本账号。
   - 支持非流式和流式响应。
   - 无可用账号时返回 OpenAI 兼容错误，而不是触发上游请求。
5. 新增测试覆盖：
   - fake upstream conversation SSE。
   - HTTP 层 `/v1/chat/completions` 非流式协议转换。
   - 继续覆盖 `/v1/models` 和账号刷新基础路径。

已验证：

```bash
cd go-backend
go test ./...
```

结果：全部通过。

真实网络烟测：

- 使用临时 `CHATGPT2API_AUTH_KEY` 和临时 `data` 目录启动 Go 服务。
- `GET /health` 返回正常。
- `GET /v1/models` 真实访问 ChatGPT 上游成功。
- 返回模型数量为 10，包含 `gpt-5` 与 `auto`。
- `POST /v1/chat/completions` 在没有账号 token 时返回预期的
  `no available account` 错误，不会误打上游。
- 烟测结束后已停止临时 Go 进程。

本轮仍缺少的真实验证：

- 本机仍未检测到 `CHATGPT2API_TEST_ACCESS_TOKEN`、
  `CHATGPT2API_ACCESS_TOKEN`、`OPENAI_ACCESS_TOKEN` 或
  `CHATGPT_ACCESS_TOKEN`。
- 因此还不能真实验证：
  - 账号刷新 `/backend-api/me`。
  - 账号刷新 `/backend-api/conversation/init`。
  - 文本生成 `/backend-api/sentinel/chat-requirements`。
  - 文本生成 `/backend-api/conversation`。

下一步要继续真实验证时，请提供一个测试账号的 `access_token`。
推荐只通过临时环境变量 `CHATGPT2API_TEST_ACCESS_TOKEN` 提供，
不要写入仓库文件，也不要粘贴到文档或日志里。

已完成 Docker Desktop 验证切片：

1. 新增 `go-backend/Dockerfile`。
2. 新增 `docker-compose.go.yml`：
   - 独立服务名 `go-backend`。
   - 独立容器名 `chatgpt2api-go-backend`。
   - 默认映射 `127.0.0.1:8001`。
   - 挂载当前 `config.json`、`VERSION` 和 `data`。
   - 支持 `CHATGPT2API_AUTH_KEY`、`CHATGPT2API_PROXY`、
     `GO_BACKEND_PORT` 环境变量。
3. 新增 `docs/go-backend-docker.md`：
   - Docker Desktop 启动命令。
   - 基础健康检查。
   - 账号 token 导入方式。
   - 账号刷新和文本生成真实验证命令。

Docker 旁路服务的设计目标：

- 不替换现有 Python/Web 容器。
- 不修改当前 Dockerfile。
- 让用户可以在 Docker Desktop 中保持 Go 后端运行，
  通过管理 API 导入测试账号后继续做真实上游验证。

已调整为 Go 单容器 Web 入口：

1. Go 验证镜像现在打包原来的 Next 静态网页。
2. 容器内采用双进程：
   - `nginx` 监听容器 `80` 端口，服务原 Web 页面。
   - `chatgpt2api-go` 监听容器本地 `8001` 端口，提供 Go API。
3. `docker-compose.go.yml` 对外仍默认暴露本机 `8001`，
   但实际映射到容器 `80`。
4. `nginx` 将这些路径反代到 Go 后端：
   - `/auth/*`
   - `/api/*`
   - `/v1/*`
   - `/health`
   - `/version`
5. 其他路径使用原前端静态页面 fallback，因此浏览器访问
   `http://127.0.0.1:8001/` 不应再出现纯 Go API 的 `404 page not found`。
6. 收紧 `.dockerignore`，避免把 `data`、缓存、临时目录和日志带入构建上下文。

本轮已验证：

```bash
cd web
npm run build

cd ../go-backend
go test ./...
```

结果：全部通过。

Docker Desktop 真实启动验证状态：

- Docker Desktop 重启后，执行
  `docker compose -f docker-compose.go.yml up --build -d --force-recreate`
  成功。
- 新容器状态为 `healthy`，端口映射为 `0.0.0.0:8001->80/tcp`。
- `GET http://127.0.0.1:8001/` 返回 `200` 和原 Next 静态页面，
  已修复根路径 `404 page not found`。
- `GET /login/` 返回 `200`。
- Playwright 实测浏览器打开 `/` 后跳转到 `/login/`，
  使用默认密钥登录后进入 `/accounts/`。
- `POST /auth/login` 返回管理员身份。
- `GET /api/accounts` 返回成功，当前账号数为 `0`。
- `GET /v1/models` 真实访问 ChatGPT 上游成功，返回 10 个模型，
  包含 `auto` 与 `gpt-5`。
- `POST /v1/chat/completions` 在无账号时返回预期的
  `503 no_available_account`，不会误打上游。

当前仍缺少真实文本生成验证：

- `data/accounts.json` 为空。
- 用户需要先在 Web 页面导入一个测试账号 token。
- 导入后继续验证账号刷新、`/backend-api/me`、
  `/backend-api/conversation/init`、sentinel requirements、
  conversation SSE 和真实文本生成。


## 🎯 本次目标

本计划书解决三个问题：

1. 判断当前后端是否适合用 Go 重写。
2. 明确外部 Go 项目哪些设计值得参考，哪些不适合直接照搬。
3. 给出可分阶段执行的迁移路线，先做一部分核心功能验证资源占用和响应速度。

本计划书不包含直接代码迁移，不改当前运行逻辑。


## ✅ 完成标准

后续启动 Go 重写时，建议把下面几项作为阶段验收标准：

1. Go 服务可以独立启动，并能读取当前项目的 `config.json` 与 `data`
   目录数据。
2. 先支持核心接口：
   - `POST /auth/login`
   - `GET /api/accounts`
   - `GET /api/image-tasks`
   - `POST /api/image-tasks/generations`
   - `GET /api/creation-tasks`
   - `POST /api/creation-tasks/image-generations`
   - `POST /api/accounts`
   - `DELETE /api/accounts`
   - `POST /api/accounts/refresh`
   - `GET /v1/models`
   - `POST /v1/chat/completions`
   - `POST /v1/images/generations`
   - `POST /v1/images/generations` 同时支持 `url` 与 `b64_json`
3. 同一批账号下，对比 Python 后端和 Go 后端的 CPU、内存、P95 延迟、错误率。
4. 出问题时可以立即切回 Python 后端，账号数据不丢失。


## 🔍 当前 Python 后端观察

当前项目是：

```text
[客户端 / OpenAI SDK / Cherry Studio / New API]
                 ↓
          [FastAPI + Uvicorn]
                 ↓
      [ChatGPT Web backend / 文件接口 / 图片接口]
                 ↓
      [JSON / 数据库 / Git / 图片存储 / 日志]
```

主要后端模块：

- `api/app.py`：FastAPI 应用入口。
- `api/ai.py`：OpenAI 兼容接口入口。
- `api/accounts.py`：账号池接口。
- `api/image_tasks.py`：图片任务接口。
- `services/account_service.py`：账号管理、刷新、额度、轮询。
- `services/openai_backend_api.py`：ChatGPT Web 后端逆向请求。
- `services/protocol/conversation.py`：SSE conversation 协议解析与 OpenAI 格式转换。
- `services/storage/*.py`：JSON、数据库、Git 存储后端。
- `services/image_task_service.py`、`services/imgbed_service.py`：任务与图片保存。


## ⚠️ 当前瓶颈判断

用户反馈“资源占用有时候很夸张，账号一多响应慢”，从代码结构看是合理的。

主要压力点有这些：

1. **同步网络请求较多**

   当前核心上游请求依赖 `curl-cffi` 同步调用。
   FastAPI 层再通过 `run_in_threadpool` 或生成器桥接异步响应。
   账号多、请求多时，会产生较多线程、连接、上下文切换和阻塞等待。

2. **账号刷新链路重**

   单个账号刷新会请求：

   - `/backend-api/me`
   - `/backend-api/conversation/init`
   - 部分场景还会请求账号检查接口

   批量刷新虽然使用 `ThreadPoolExecutor(max_workers=10)`，但账号很多时依然会造成
   CPU、内存、连接数和上游限流压力。

3. **图片链路天然较重**

   图片生成涉及：

   - 获取 sentinel requirements
   - PoW / Turnstile 相关处理
   - SSE 流式读取
   - 轮询 conversation 找图片结果
   - 下载图片
   - base64 编码
   - 图片保存或图床上传

   这些步骤在 Python 中大多是同步 IO 和迭代器串联，压力上来后资源波动明显。

4. **账号选择存在实时探活成本**

   当前图片账号选择链路中，`get_available_access_token()` 会调用
   `refresh_account_state()`，也就是选账号时可能触发远程探活。
   这会让请求响应和账号刷新耦合，账号越多、异常账号越多，尾延迟越明显。

5. **JSON 与日志写入需要控制频率**

   当前账号状态、成功失败次数、图片任务、日志等都会写入存储。
   如果很多请求同时更新同一批 JSON 文件，锁和磁盘 IO 会放大延迟。


## 📚 外部 Go 实现研究结论

研究对象：

```text
https://github.com/jiujiu532/chatgpt2api-go
```

```text
https://github.com/ZyphrZero/chatgpt2api
```

本地研究快照：

```text
C:\Users\lucky0401\AppData\Local\Temp\chatgpt2api-go-research
```

外部 Go 项目不是简单 demo，而是一个完整的 Go 单体服务。

主要结构：

```text
internal/
├── main.go
├── httpapi/
├── service/
├── backend/
├── protocol/
├── storage/
└── config/
```

关键依赖：

- `github.com/enetx/surf`
- `github.com/enetx/g`
- `github.com/refraction-networking/utls`
- `modernc.org/sqlite`
- `github.com/lib/pq`
- `github.com/go-sql-driver/mysql`

这说明它已经在 Go 里处理了浏览器指纹和 TLS 指纹问题。
这是 Go 重写最值得参考的部分。

`ZyphrZero/chatgpt2api` 更接近完整产品形态，值得参考的点是：

- Go 单体服务，容器内直接托管前端。
- OpenAI 兼容图片生成、图片编辑、Responses、Messages。
- `/api/creation-tasks` 异步创作任务体系。
- 账号池导入、刷新、清理和筛选。
- JSON、SQLite、PostgreSQL 存储。
- 管理端 RBAC、日志、图片库、设置页。
- Docker 部署、版本检查和在线更新。

不建议直接照搬的点是：

- 前端和 RBAC 体系与当前项目不完全一致。
- 当前项目第一阶段仍应保留原 Web 资产和现有登录方式。
- 它的管理端规模更重，容易把首期范围拉大。


## ✨ 外部 Go 项目值得参考的设计

### 1. 浏览器指纹客户端

外部项目在 `service/proxy.go` 中使用：

```go
surf.NewClient().
    Builder().
    SecureTLS().
    Impersonate().
    Session().
    Timeout(timeout)
```

并通过 `uTLS` 间接提供浏览器 TLS 指纹能力。

这点很关键，因为当前 Python 项目用 `curl-cffi` 的核心原因也是要模拟浏览器请求。
如果 Go 重写不能稳定替代这部分，其他优化价值会打折。

可参考能力：

- Chrome / Firefox 指纹。
- Windows / macOS / Linux / Android / iOS profile。
- HTTP / HTTPS / SOCKS5 / SOCKS5H 代理。
- 每个账号可保留 `user-agent`、`impersonate`、`oai-device-id`、
  `oai-session-id` 等指纹字段。


### 2. 账号选择不实时刷新

外部项目的 `GetAvailableAccessTokenFor(ctx, allow)` 更偏向从本地缓存状态选账号，
而不是每次请求前都打上游刷新。

这对当前项目很重要。

推荐迁移方向：

- 请求路径只从本地缓存选择可用账号。
- 账号刷新放到后台 watcher 和手动刷新接口。
- 遇到真实请求失败时再标记账号异常、限流或失败次数。

这样能显著降低账号多时的响应尾延迟。


### 3. 图片账号并发槽位

外部项目有 `imageReservations map[string]int`，用于控制图片账号并发占用。

大意是：

- 一个账号还有多少图片额度，就给多少并发槽。
- 如果额度未知，则给一个有限默认并发，例如 3。
- 请求开始时预留槽位。
- 请求结束后释放槽位。
- 成功扣额度，失败记失败次数。

这比单纯轮询账号更稳。
它能避免多个图片请求同时打到同一个账号，导致同号并发过高、误判限流或上游失败。


### 4. Go `context.Context` 贯穿 SSE

外部项目的 `backend.Client.StreamConversation(ctx, ...)` 和协议层都接收
`context.Context`。

这意味着：

- 客户端断开后可以及时取消上游请求。
- 图片多输出并发失败时可以取消其他 goroutine。
- 超时、重试、任务取消可以统一管理。

当前 Python 的生成器式 SSE 也能工作，但取消传播和资源释放更难做干净。


### 5. 协议转换集中到 Engine

外部项目把 OpenAI 兼容转换集中在 `protocol.Engine`：

- `/v1/models`
- `/v1/chat/completions`
- `/v1/images/generations`
- `/v1/images/edits`
- `/v1/responses`
- `/v1/messages`

这个结构清晰，适合当前项目参考。

当前 Python 项目也已经有 `services/protocol/*`，迁移时可以按现有模块一一对应，
不要把协议转换散落到 HTTP handler 里。


## 📌 已完成图片生成切片

当前仓库的 Go 试做已经把图片生成核心链路跑通，并做过真实验证。

已实现：

- `POST /v1/images/generations`
- `GET /api/image-tasks`
- `POST /api/image-tasks/generations`
- `GET /api/creation-tasks`
- `POST /api/creation-tasks/image-generations`

已支持：

- `response_format=url`
- `response_format=b64_json`
- `gpt-image-2` 到 `gpt-5-5` 的模型映射
- 真实账号图片生成任务回填和下载结果处理

当前边界：

- `POST /api/image-tasks/edits`
- `POST /api/creation-tasks/image-edits`
- `POST /v1/images/edits`

这三条仍返回明确 `501`，图生图会留到下一阶段迁移。


### 6. 存储后端抽象

外部项目的 `storage.Backend` 支持：

- JSON
- SQLite
- PostgreSQL
- MySQL

当前项目也有 JSON、数据库、Git 存储抽象。

迁移时建议先保持当前数据格式兼容，不急着切默认 SQLite。
等核心接口稳定后，再讨论数据库化。


## 🚫 不建议直接照搬的部分

1. **不建议直接替换成外部项目整仓**

   当前项目是 Python 后端加 Next.js 管理端。
   外部项目是 Go 单体加 Vite React 管理端。
   管理端、鉴权、配置、存储和功能边界都不完全一致。

2. **不建议第一阶段迁移 RBAC**

   外部项目有本地用户、RBAC、权限模型。
   当前项目主要是简单登录和 Bearer Key。
   第一阶段应先兼容现有鉴权，避免管理端和客户端同时改。

3. **不建议第一阶段强制改 `.env` 配置模型**

   当前项目以 `config.json` 为主，并支持环境变量覆盖。
   第一阶段 Go 服务应读取当前 `config.json`，否则用户升级成本过高。

4. **不建议第一阶段迁移所有周边功能**

   例如：

   - CPA
   - sub2api
   - 注册
   - 备份
   - Git 存储
   - 图床配置
   - 图片历史管理后台

   这些可以后移。
   先证明核心代理能力和资源收益。


## 🧭 推荐路线：旁路 Go 核心服务

推荐先做：

```text
[客户端]
   ↓
[Go 核心后端，独立端口]
   ↓
[ChatGPT Web backend]

[Next.js 管理端]
   ↓
[现有 Python 后端，继续保留]
```

这样有几个好处：

1. 不破坏现有 Python 后端。
2. 可以让部分客户端或测试流量指向 Go 服务。
3. Go 服务失败可以立即切回 Python。
4. 可以真实比较资源占用，不靠猜测。

后续验证稳定后，再逐步让管理端也走 Go API。


## 🏗️ 推荐 Go 后端结构

建议在当前仓库新增独立目录，例如：

```text
go-backend/
├── cmd/
│   └── chatgpt2api/
│       └── main.go
├── internal/
│   ├── httpapi/
│   ├── config/
│   ├── storage/
│   ├── account/
│   ├── upstream/
│   ├── protocol/
│   ├── image/
│   ├── auth/
│   └── observability/
├── go.mod
└── README.md
```

模块职责：

- `httpapi`：路由、请求解析、响应输出、SSE。
- `config`：读取当前 `config.json` 和环境变量。
- `storage`：读取与保存 `data/accounts.json`、`data/auth_keys.json` 等。
- `account`：账号池、刷新、状态机、并发槽位。
- `upstream`：ChatGPT Web 请求、浏览器指纹、PoW、Turnstile、文件下载。
- `protocol`：OpenAI / Anthropic 兼容协议转换。
- `image`：图片结果解析、保存、格式转换、历史记录。
- `auth`：管理端登录、API Key 校验。
- `observability`：日志、指标、pprof、请求耗时。


## 🪜 阶段计划

### 阶段 0：基准测试与问题固化

目标：先量化当前 Python 后端的真实瓶颈，避免 Go 重写后无法证明收益。

工作内容：

1. 固定一组测试账号规模：
   - 10 个账号
   - 50 个账号
   - 100 个账号
   - 300 个账号，如果本地条件允许
2. 固定一组测试接口：
   - `GET /api/accounts`
   - `POST /api/accounts/refresh`
   - `GET /v1/models`
   - `POST /v1/chat/completions`
   - `POST /v1/images/generations`
3. 记录指标：
   - 进程启动内存
   - 空闲内存
   - 刷新账号峰值内存
   - CPU 峰值
   - P50 / P95 / P99 延迟
   - 错误率
   - 上游 401 / 429 / 5xx 数量
4. 输出一份 `docs/backend-baseline.md`。

验收标准：

- 能复现“账号多响应慢”的场景。
- 有 Go 重写前的对比基线。

预估耗时：0.5 到 1 天。


### 阶段 1：Go 服务骨架与配置兼容

目标：Go 服务能独立启动，提供基础路由和当前配置读取能力。

工作内容：

1. 新增 `go-backend` 工程。
2. 使用标准库 `net/http` 或轻量路由，不引入重框架。
3. 读取当前项目：
   - `config.json`
   - `CHATGPT2API_*` 环境变量
   - `data` 目录位置
4. 实现：
   - `GET /health`
   - `POST /auth/login`
   - Bearer Key 校验中间件
   - 统一 OpenAI 错误格式
   - 请求日志脱敏
5. 增加基础单元测试。

验收标准：

- Go 服务可在独立端口启动。
- 当前管理密码和 API Key 可被识别。
- 错误响应格式与现有 Python 尽量一致。

预估耗时：1 到 2 天。


### 阶段 2：账号池与存储兼容

目标：Go 服务能读取、展示、刷新和更新当前账号池。

工作内容：

1. 兼容 `data/accounts.json` 字段：
   - `access_token`
   - `refresh_token`
   - `type`
   - `status`
   - `quota`
   - `image_quota_unknown`
   - `email`
   - `user_id`
   - `limits_progress`
   - `default_model_slug`
   - `restore_at`
   - `success`
   - `fail`
   - `last_used_at`
   - `fp`
   - `user-agent`
   - `impersonate`
   - `oai-device-id`
   - `oai-session-id`
2. 实现账号接口：
   - `GET /api/accounts`
   - `POST /api/accounts`
   - `DELETE /api/accounts`
   - `POST /api/accounts/refresh`
   - `POST /api/accounts/update`
3. 实现本地缓存账号选择。
4. 实现图片账号并发槽位。
5. 实现后台限流账号 watcher。
6. 控制 JSON 写入频率，避免每个小状态都立即全量落盘。

验收标准：

- 当前管理端账号列表接口可以对接 Go 服务。
- 100 个账号刷新时不会阻塞所有请求。
- 请求路径不再因为选账号而批量探活上游。

预估耗时：2 到 4 天。


### 阶段 3：上游浏览器指纹客户端

目标：在 Go 中稳定替代 Python 的 `curl-cffi` 上游访问能力。

工作内容：

1. 引入 `surf` 或等价方案。
2. 支持 `uTLS` 浏览器指纹。
3. 支持代理：
   - HTTP
   - HTTPS
   - SOCKS5
   - SOCKS5H
4. 支持账号级指纹字段：
   - `user-agent`
   - `impersonate`
   - `sec-ch-ua`
   - `sec-ch-ua-platform`
   - `oai-device-id`
   - `oai-session-id`
5. 实现上游基础接口：
   - `/`
   - `/backend-api/me`
   - `/backend-api/conversation/init`
   - `/backend-api/sentinel/chat-requirements`
   - `/backend-api/conversation`
6. 实现 PoW 资源解析和 proof token 生成。
7. 对 Turnstile 保留现有能力边界，不先扩大范围。

验收标准：

- 少量真实账号能稳定刷新。
- `/v1/models` 能通过 Go 服务返回。
- 文本 conversation SSE 能跑通。

预估耗时：3 到 6 天。

本阶段是最大风险点。
如果 Go 的 TLS 指纹无法稳定通过上游检查，需要先停下来评估替代方案。


### 阶段 4：文本接口迁移

目标：完成文本相关 OpenAI 兼容接口。

工作内容：

1. 实现 `GET /v1/models`。
2. 实现 `POST /v1/chat/completions`。
3. 支持非流式和流式响应。
4. 支持基础 messages 转换：
   - system
   - user
   - assistant
   - 文本 content
5. 支持全局 system prompt。
6. 支持 token 失败后的换号策略。
7. 接入请求日志与错误脱敏。

验收标准：

- OpenAI SDK 可直接调用 Go 服务。
- 流式输出首包、增量、结束事件正常。
- 断开客户端连接后，上游请求能被取消。

预估耗时：2 到 4 天。


### 阶段 5：图片生成核心链路

目标：优先迁移当前最重、最值得优化的图片生成链路。

当前仓库的 Go 试做已经把这一阶段的核心文生图链路跑通，
并真实验证了 `url` 和 `b64_json` 两种返回格式。
图生图仍留到阶段 6。

工作内容：

1. 实现 `POST /v1/images/generations`。
2. 支持参数：
   - `prompt`
   - `model`
   - `n`
   - `size`
   - `response_format`
   - `stream`
3. 实现图片模型映射：
   - `gpt-image-2`
   - `codex-gpt-image-2`
   - `auto`
4. 实现图片 SSE 解析。
5. 实现 conversation 轮询图片结果。
6. 实现图片下载、base64、URL 保存。
7. 实现账号并发槽位：
   - 成功扣额度。
   - 失败计数。
   - 限流标记。
   - token 无效自动剔除或标记异常。
8. `n > 1` 时使用 goroutine 并发处理，但要受账号槽位限制。

验收标准：

- 常规文生图能返回 URL 或 b64。
- `n > 1` 不会把所有请求压到同一个账号。
- 客户端断开后，Go 服务能取消未完成的上游请求。
- 同等账号数量下，内存峰值和 P95 延迟明显优于 Python 基线。

预估耗时：4 到 8 天。


### 阶段 6：图片编辑、Responses、Messages 与异步任务

目标：补齐主要兼容协议。

工作内容：

1. 实现 `POST /v1/images/edits`。
2. 实现 `POST /v1/responses`。
3. 实现 `POST /v1/messages`。
4. 迁移图片编辑上传逻辑。
5. 迁移 response image tool 调用。
6. 迁移 Anthropic Messages 兼容层。
7. 补齐 `/api/image-tasks/edits`、`/api/creation-tasks/image-edits`
   和 `/v1/images/edits`，或保留 Python 任务服务作为回退。

验收标准：

- Cherry Studio、New API 等主要接入路径不回退。
- 当前 Python 测试用例能改造成对 Go 服务的契约测试。

预估耗时：1 到 2 周。


### 阶段 7：管理端、部署与默认切换

目标：让 Go 后端成为默认服务，Python 后端可作为回滚方案。

工作内容：

1. 管理端 API 指向 Go 服务。
2. 迁移日志、设置、图床、备份、CPA、sub2api 等周边功能。
3. Dockerfile 与 docker-compose 改造。
4. 增加运行模式：
   - `backend=python`
   - `backend=go`
   - `backend=hybrid`
5. 增加数据迁移脚本与备份脚本。

验收标准：

- 新部署默认走 Go 后端。
- 老部署可以无损升级。
- 出问题可以通过配置回滚。

预估耗时：2 到 4 周。


## 🧪 测试方案

### 单元测试

必须覆盖：

- 配置读取。
- JSON 存储读写。
- 账号去重、增删改。
- 账号状态机。
- 图片槽位预留与释放。
- SSE payload 解析。
- OpenAI 错误格式。
- 模型名映射。


### 集成测试

使用假的上游 HTTP 服务模拟：

- 200 正常响应。
- 401 token 无效。
- 429 限流。
- 5xx 上游错误。
- SSE 正常结束。
- SSE 中途断开。
- 图片文件 ID 延迟出现。
- 图片轮询超时。


### 真实上游烟测

只用少量测试账号：

1. 刷新账号。
2. 调 `/v1/models`。
3. 调一次文本。
4. 调一次图片。
5. 测一次代理。
6. 测一次客户端中途断开。

注意：真实上游烟测不要批量压账号，避免触发风控或限流。


### 压测

推荐工具：

- `hey`
- `wrk`
- `k6`
- Go 自带 `pprof`

推荐压测维度：

- 账号数量：10、50、100、300。
- 并发请求：1、5、10、20。
- 图片 `n`：1、2、4。
- 响应模式：非流式、流式。
- 代理：无代理、HTTP 代理、SOCKS5 代理。

推荐记录：

- CPU 平均值和峰值。
- 内存常驻和峰值。
- goroutine 数。
- 打开的连接数。
- P50 / P95 / P99。
- 错误率。
- 上游状态码分布。


## 📊 性能目标

具体目标应以阶段 0 基准为准。
在没有基准前，先设相对目标：

1. 空闲内存低于 Python 后端。
2. 100 个账号刷新时，Go 服务内存峰值低于 Python 后端 40% 以上。
3. 请求路径不因选账号触发远程刷新，P95 延迟应明显下降。
4. 客户端断开后，上游连接能及时释放。
5. 图片 `n > 1` 时，并发受账号槽位控制，错误率不高于 Python 后端。

如果达不到这些目标，说明 Go 重写没有解决主要瓶颈，需要重新分析。


## 🔐 安全与数据保护

Go 重写必须保留这些规则：

1. 日志不能输出完整 `access_token`、`refresh_token`、API Key。
2. 管理端接口必须继续校验登录态。
3. OpenAI 兼容接口必须继续校验 Bearer Key。
4. 账号 JSON 写入前要有备份或原子写入。
5. 旁路验证期间，避免 Python 和 Go 同时写同一个 JSON 文件。
6. 测试和压测不要把生产 token 发到不可信服务。

旁路阶段推荐：

- Go 服务先只读账号数据，或使用数据副本。
- 确认写入兼容后，再允许 Go 服务写账号状态。
- 开启写入前先备份 `data/accounts.json`。


## 🔁 回滚方案

必须从第一阶段就设计回滚。

推荐方式：

1. Python 后端继续保留原端口。
2. Go 后端使用独立端口，例如 `8001`。
3. 客户端通过配置选择 base URL。
4. Docker Compose 中保留两个服务。
5. Go 写入数据前先备份。
6. 如果 Go 服务上游失败率升高，立即切回 Python。

切换策略：

```text
阶段 1：本地手动调用 Go 服务
阶段 2：测试客户端接 Go 服务
阶段 3：少量真实请求接 Go 服务
阶段 4：核心接口默认接 Go 服务
阶段 5：Python 只作为回滚保留
```


## 🚩 主要风险

### 风险 1：Go TLS 指纹不稳定

这是最大风险。

当前 Python 项目依赖 `curl-cffi`。
Go 侧即使使用 `surf` 和 `uTLS`，也需要真实账号验证。

应对方式：

- 阶段 3 单独验证，不和其他迁移混在一起。
- 保留账号级 `impersonate` 和浏览器字段。
- 允许按账号切换 profile。
- 出现 Cloudflare / challenge 问题时优先调整指纹和代理。


### 风险 2：ChatGPT Web 协议变化快

上游字段、接口、PoW、图片结果格式都可能变。

应对方式：

- 协议解析保持容错。
- SSE 原始事件可记录脱敏样本。
- 保留 Python 实现作为对照。
- 将上游协议封装在 `upstream` 和 `protocol`，避免散落。


### 风险 3：账号状态并发写错

Go 并发更容易写出高并发，但也更容易出现状态竞争。

应对方式：

- 账号状态统一由 `account.Service` 管理。
- 所有状态更新走同一把锁或单线程事件队列。
- JSON 写入使用原子写。
- 图片槽位必须保证请求结束后释放。


### 风险 4：功能迁移范围失控

当前项目周边功能不少。
如果一开始就迁移全部功能，周期会拉长，风险也会叠加。

应对方式：

- 第一阶段只做核心代理链路。
- 管理端和周边服务后移。
- 每阶段都有可运行成果和回滚点。


## 🚀 首期建议试做范围

我建议第一批只做“Go 核心旁路服务”，范围如下：

1. 新增 `go-backend`。
2. 读取当前 `config.json`。
3. 读取当前 `data/accounts.json` 和 `data/auth_keys.json`。
4. 支持登录和 Bearer Key 校验。
5. 支持账号列表、添加、删除、刷新。
6. 支持本地缓存选账号，不在请求路径实时刷新。
7. 支持 `/v1/models`。
8. 支持 `/v1/chat/completions` 文本流式。
9. 支持 `/v1/images/generations` 文生图。
10. 支持 `/api/image-tasks` 和 `/api/creation-tasks` 任务别名。
11. 输出基准对比报告。

暂不做：

- 注册。
- CPA。
- sub2api。
- 备份。
- Git 存储。
- 完整 RBAC。
- 管理端重写。
- 图片历史全量迁移。

这样第一批工作能聚焦在用户最关心的问题：

- 账号多时响应是否变快。
- CPU 和内存是否下降。
- 图片请求是否更稳。


## ⏱️ 时间预估

首期旁路验证：

- 基准测试：0.5 到 1 天。
- Go 骨架：1 到 2 天。
- 账号池和存储兼容：2 到 4 天。
- 上游浏览器指纹客户端：3 到 6 天。
- 文本接口：2 到 4 天。
- 图片生成：4 到 8 天。
- 压测与修复：2 到 4 天。

首期合计：大约 2 到 4 周。

完整替换：

- 在首期成功基础上，继续迁移图片编辑、Responses、Messages、任务、管理端和周边能力。
- 大约 4 到 8 周，取决于上游协议稳定性和兼容范围。


## 📍 后续规划

当前这一版已经能作为可部署的 Go 核心后端使用，后续建议按下面顺序继续：

1. 补齐图生图 `/v1/images/edits` 和异步编辑任务。
2. 把图片历史、图片库、日志页、设置页相关 API 继续迁移。
3. 补完 Responses、Messages 和更完整的 OpenAI 兼容层。
4. 迁移 CPA、Sub2API、注册、备份、图床等周边能力。
5. 做正式压测，对比 Python 后端和 Go 后端的 CPU、内存、P95、错误率。
6. 视压测结果决定是否把 Go 作为默认后端。


## ✅ 最终建议

Go 重写值得做，但应该按“验证核心收益”推进。

最推荐的下一步是：

1. 先做阶段 0，量化当前 Python 后端在账号多时的 CPU、内存和 P95。
2. 再做首期 Go 旁路服务。
3. 用同一批账号和同一组请求做 A/B 对比。
4. 如果 Go 在核心链路上确实明显更稳、更省资源，再继续迁移完整后端。

判断是否继续全量迁移，不应该只看“Go 更快”这个理论结论。
应该看真实指标：

- Go 的 TLS 指纹是否稳定。
- 账号多时 P95 是否下降。
- 图片链路错误率是否不升高。
- 内存和 CPU 是否明显改善。
- 当前数据和管理端是否能平滑兼容。

只要这些指标成立，就可以继续推进完整 Go 后端。
如果 TLS 指纹或上游协议稳定性不达标，应保留 Python 后端，并只把账号调度、
任务队列、图片结果处理等局部能力用 Go 旁路优化。
