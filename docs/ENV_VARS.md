# 环境变量参考

所有变量可通过两种方式提供，优先级：**进程环境变量 > `.env` 文件**。

## 加载规则

- 启动时读同目录 `.env`
- 每一行 `KEY=VALUE`，值两侧自动去空格，引号不需要转义
- 以 `#` 开头的行为注释
- 进程环境里已经存在的 key 不会被 `.env` 覆盖

## 完整变量表

### Server

| 变量 | 默认值 | 说明 |
|---|---|---|
| `PORT` | `3003` | HTTP 监听端口 |
| `API_KEY` | （空） | `/v1/chat/completions` 和 `/v1/messages` 的访问密钥。**留空 = 开放**（谁都能调） |
| `DASHBOARD_PASSWORD` | （空） | `/dashboard/api/*` 的访问密钥。留空时自动回退到 `API_KEY` |
| `CORS_ALLOWED_ORIGINS` | （空） | 跨域白名单。留空 = 不发 CORS 头 / `*` = 通配 / 逗号分隔的多个 origin = 精确匹配 |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

### Language Server

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LS_BINARY_PATH` | （空，自动查找） | 显式指定二进制路径。留空时按 `<exe 同目录>/bin/language_server_linux_x64` → `/opt/windsurf/language_server_linux_x64` 顺序查找 |
| `LS_PORT` | `42100` | LS 默认 gRPC 端口。多代理时后续实例从 `LS_PORT+1` 开始自增 |

### Codeium / Windsurf 上游

| 变量 | 默认值 | 说明 |
|---|---|---|
| `CODEIUM_API_URL` | `https://server.self-serve.windsurf.com` | Windsurf 官方 gRPC/REST 接入点。**除非自己搭中转否则不要改** |
| `CODEIUM_API_KEY` | （空） | 启动时自动导入的账号 API Key，逗号分隔多个 |
| `CODEIUM_AUTH_TOKEN` | （空） | 启动时自动导入的 Auth Token，逗号分隔多个。内部会换成 API Key |

### 默认模型 / 请求

| 变量 | 默认值 | 说明 |
|---|---|---|
| `DEFAULT_MODEL` | `claude-4.5-sonnet-thinking` | 调用端没传 `model` 字段时使用 |
| `MAX_TOKENS` | `8192` | 请求里缺 `max_tokens` 时的默认值 |

## 示例 `.env`

生产最小配置：

```bash
PORT=3003
API_KEY=sk-your-secret-here
DASHBOARD_PASSWORD=another-secret

LS_BINARY_PATH=
LS_PORT=42100

DEFAULT_MODEL=claude-4.5-sonnet-thinking
MAX_TOKENS=8192
LOG_LEVEL=info
```

允许特定前端跨域：

```bash
CORS_ALLOWED_ORIGINS=https://chat.example.com,https://dashboard.example.com
```

启动时预装账号：

```bash
CODEIUM_AUTH_TOKEN=wsat_xxxxxx,wsat_yyyyyy
```

## 安全

- **永远不要** 把 `.env` 提交到 git。仓库的 `.gitignore` 里 `.env` 已列入忽略
- 生产部署 `chmod 600 .env`，仅 windsurfapi 专用用户可读
- `API_KEY` 写日志时会被 `logx` 自动脱敏（只保留前 8 位），但 `.env` 文件本身请自行加密或放到 systemd `LoadCredential=` 里
- Firebase API Key（`AIzaSy...`）是 Windsurf 官方项目的固定公开值，不通过环境变量暴露，写死在代码里

## 运行时配置 vs 环境变量

**环境变量**只在进程启动时读一次，修改后需要重启。

以下配置持久化在磁盘 JSON 文件，**通过 Dashboard UI 或 API 热修改，无需重启**：
- `accounts.json` —— 账号池（含限速/限流窗口、层级、能力）
- `proxy.json` —— 全局与账号级代理
- `runtime-config.json` —— 实验性开关（Cascade 对话复用、模型身份注入模板）
- `model-access.json` —— 全局模型白/黑名单
- `stats.json` —— 统计桶（重启恢复）

想把这些也放进环境变量（不可写）、彻底只读部署？当前版本不支持，未来可以通过添加 `-config-readonly` 启动参数实现。
