# HTTP API 参考

## 目录

- [基础说明](#基础说明)
- [公开接口](#公开接口)
- [OpenAI 兼容](#openai-兼容)
- [Anthropic 兼容](#anthropic-兼容)
- [账号管理（开放 API）](#账号管理开放-api)
- [Dashboard 控制台 API](#dashboard-控制台-api)

## 基础说明

### 认证

| 接口前缀 | 认证方式 | 密钥来源 |
|---|---|---|
| `/v1/*` | `Authorization: Bearer <API_KEY>` | 环境变量 `API_KEY` |
| `/auth/*` | 同上 | 同上 |
| `/dashboard/api/*` | `X-Dashboard-Password: <password>` | 环境变量 `DASHBOARD_PASSWORD`，留空时回退到 `API_KEY` |
| `/health` | 无 | — |
| `/dashboard` | 无（页面自身走 Pinia store + `/dashboard/api/auth` 再认证） | — |

`API_KEY` 环境变量留空时 `/v1/*` 与 `/auth/*` 也开放；`DASHBOARD_PASSWORD` 和 `API_KEY` 都留空时 `/dashboard/api/*` 也开放（**仅用于本地开发**，生产必须二者之一）。

### 响应规范

- 成功：`200 OK` + JSON body
- 业务失败：`4xx` + `{"error": "..."}` JSON body
- 无权限：`401 Unauthorized` + `{"error": "Unauthorized. Set X-Dashboard-Password header."}`
- SSE：`Content-Type: text/event-stream`，事件帧 `data: <json>\n\n`，每 15 秒 `: heartbeat` 保活

### 请求体大小上限

| 接口 | 上限 |
|---|---|
| `/v1/chat/completions` / `/v1/messages` | 32 MB |
| `/dashboard/api/*` POST/PUT/PATCH | 1 MB |

## 公开接口

### `GET /health`

不需认证，返回服务状态。

**请求**：无参数

**响应示例**：

```json
{
  "status": "ok",
  "provider": "WindsurfAPI bydwgx1337",
  "version": "1.2.0-go",
  "uptime": 123456,
  "ls": {
    "running": true,
    "pid": 9722,
    "port": 42100,
    "startedAt": 1776752567597,
    "restartCount": 0,
    "instances": [{"key":"default","port":42100,"pid":9722,"ready":true}]
  },
  "accounts": {"total": 15, "active": 15, "error": 0}
}
```

## OpenAI 兼容

### `POST /v1/chat/completions`

完全兼容 OpenAI ChatCompletions 协议。

**关键字段**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `model` | string | 模型 ID，必填（缺省使用 `DEFAULT_MODEL`） |
| `messages` | array | 与 OpenAI 一致，支持 `system` / `user` / `assistant` / `tool` 四种角色 |
| `stream` | bool | 是否流式。默认 `false` |
| `max_tokens` | int | 缺省使用 `MAX_TOKENS` |
| `temperature` | float | 透传 |
| `tools` | array | OpenAI `tools[]`，通过文本协议模拟（见 CLAUDE.md 的 tool emulation 说明） |
| `tool_choice` | string / object | 透传 |

**流式响应**：SSE `data:` 帧，包含 `choices[0].delta.content` 增量；结尾 `data: [DONE]`。

**非流式响应**：标准 OpenAI 形状，`choices[0].message` + `usage`。

**usage 字段**：

```json
{
  "prompt_tokens": 123,       // 等于 input + cacheRead + cacheWrite
  "completion_tokens": 456,
  "total_tokens": 579,
  "prompt_tokens_details": {
    "cached_tokens": 0        // cacheRead
  }
}
```

### `GET /v1/models`

列出所有可用模型。响应示例：

```json
{
  "object": "list",
  "data": [
    {"id": "claude-opus-4.6", "object": "model", "owned_by": "anthropic"},
    {"id": "gpt-5-high",      "object": "model", "owned_by": "openai"}
  ]
}
```

可用模型的范围：**模型目录总集合 ∩ 全局模型访问策略**（白名单/黑名单/全部允许）。

## Anthropic 兼容

### `POST /v1/messages`

兼容 Anthropic Messages API，内部把请求转 OpenAI 形状发给账号池，流式响应再翻译回 Anthropic 的 `message_start` / `content_block_delta` / `message_delta` / `message_stop` 事件序列。

**关键字段**：

| 字段 | 类型 | 说明 |
|---|---|---|
| `model` | string | 必填 |
| `max_tokens` | int | 必填（Anthropic 语义） |
| `messages` | array | 支持 `user` / `assistant` + 内容块（`text` / `tool_use` / `tool_result`）|
| `system` | string / array | 系统提示 |
| `stream` | bool | 默认 `false` |
| `tools` | array | 原生 Anthropic tools 定义，内部转成 OpenAI tools[] |

**响应 usage**：

```json
{
  "input_tokens": 123,
  "output_tokens": 456,
  "cache_creation_input_tokens": 0,
  "cache_read_input_tokens": 0
}
```

## 账号管理（开放 API）

无需 Dashboard 密码，但受 `API_KEY` 保护（留空则开放）。

### `POST /auth/login`

添加账号，返回 `{id, email, status}`。三种请求体二选一：

```json
{"api_key": "sk-wsa-..."}
{"token": "wsat_..."}
{"email": "a@b.com", "password": "..."}
```

批量模式：

```json
{"accounts": [
  {"token": "wsat_a"},
  {"token": "wsat_b"},
  {"email": "c@d.com", "password": "..."}
]}
```

### `GET /auth/accounts`

列出所有账号（Key 字段脱敏为前 8 位）。

### `DELETE /auth/accounts/:id`

删除账号。

### `GET /auth/status`

返回认证池概要：

```json
{"authenticated": true, "accounts": {"total": 15, "active": 15, "error": 0}}
```

## Dashboard 控制台 API

全部以 `/dashboard/api/` 为前缀，详见 Accounts.vue / Proxy.vue 等前端源码的调用点。概要：

| 路径 | 方法 | 说明 |
|---|---|---|
| `/auth` | GET | 探测 Dashboard 是否需要密码、当前是否通过 |
| `/overview` | GET | 仪表盘聚合数据 |
| `/config` | GET | 服务端启动配置（脱敏） |
| `/cache` | GET / DELETE | 响应缓存状态 / 清空 |
| `/stats` | GET / DELETE | 统计数据 / 重置 |
| `/logs` | GET | 最近日志（ring buffer） |
| `/logs/stream` | GET (SSE) | 实时日志流；EventSource 不能设 header，用 `?pw=<password>` 查询参数 |
| `/accounts` | GET | 账号列表 + 共享 `tierModels` 索引 |
| `/accounts` | POST | 添加账号（形状同 `/auth/login`）|
| `/accounts/probe-all` | POST | 全部探测 |
| `/accounts/:id/probe` | POST | 单账号探测 |
| `/accounts/refresh-credits` | POST | 全部刷新余额 |
| `/accounts/:id/refresh-credits` | POST | 单账号刷新余额 |
| `/accounts/:id/rate-limit` | POST | 查询单账号的 `CheckMessageRateLimit` |
| `/accounts/:id/refresh-token` | POST | 用 refreshToken 换新 API Key |
| `/accounts/:id` | PATCH | 修改 status / label / resetErrors / blockedModels / tier |
| `/accounts/:id` | DELETE | 删除账号 |
| `/tier-access` | GET | 每个层级的可用模型表 |
| `/models` | GET | 模型目录 |
| `/model-access` | GET / PUT | 全局模型访问策略（mode + list） |
| `/model-access/add` | POST | 向当前策略列表加一个模型 |
| `/model-access/remove` | POST | 从策略列表移除 |
| `/proxy` | GET | 全局 + 账号级代理 |
| `/proxy/global` | PUT / DELETE | 全局代理 |
| `/proxy/accounts/:id` | PUT / DELETE | 账号级代理 |
| `/test-proxy` | POST | 测试代理连通性，返回出口 IP + 延迟 |
| `/experimental` | GET / PUT | 实验性开关（Cascade 对话复用等） |
| `/experimental/conversation-pool` | DELETE | 清空对话池 |
| `/identity-prompts` | GET / PUT | 模型身份注入模板（按厂商） |
| `/identity-prompts/:provider` | DELETE | 恢复该厂商的默认模板 |
| `/langserver/restart` | POST | 重启 Language Server（需 `{confirm:true}`）|
| `/self-update/check` | GET | 检查 git 更新 |
| `/self-update` | POST | 执行 `git pull` + 重启 |
| `/windsurf-login` | POST | 邮密登录 Firebase，可选自动入池 |
| `/oauth-login` | POST | OAuth idToken 换 API Key |

## 限速窗口持久化（v1.2.0+）

当上游报限速（`rate_limit_exceeded`），Go 侧会：

1. 从错误文本解析 retry-after 时长（支持 `27m31s` / `retry after 30 seconds` / `retry_after: 30`）
2. `ceil(d, 1 min) + 1 min 缓冲`（例：`5m → 6m`、`27m31s → 29m`）
3. 写入 `accounts.json`，字段 `rateLimitedUntil` + `rateLimitedStarted` + `modelRateLimits` + `modelRateStarted`
4. 选号时跳过仍在窗口内的 (账号, 模型) 对
5. 过期自动恢复；重启也保留未到期窗口

这保证了即便服务反复重启，已经被上游封了 15 分钟的账号也不会在重启 1 秒后又挨一次封。

## 错误码速查

| 场景 | HTTP | `error.type` |
|---|---|---|
| 未认证 | 401 | `authentication_error` / `Unauthorized` |
| 账号池空 | 503 | `no_accounts` |
| 所有账号被限速 | 429 | `rate_limit_exceeded`（带 `retry-after` header） |
| 上游模型不可用 | 403 | `model_not_available` |
| Language Server 不可用 | 503 | `ls_unavailable` |
| 客户端断开 | 499 | `client_cancelled` |
| 上游错误（其它） | 502 | `upstream_error` |

## 版本

当前：`1.2.0-go`。通过 `GET /health` 查看活动版本。
