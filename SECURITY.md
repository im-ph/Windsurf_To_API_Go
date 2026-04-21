# 安全说明

## 漏洞披露

发现安全问题请**不要**提交公开 issue。通过本仓库责任人的联系方式私信披露，48 小时内确认接收。

## 扫描器误报说明

### 1. Firebase apiKey（`AIzaSy...`）被标记为"密钥泄露"

**位置**：
- `internal/firebase/firebase.go:21`
- `internal/web/dist/assets/LoginTake-*.js`（Vite 构建产物内嵌）
- `README.md` 已知限制章节

**结论**：**此 key 不是 secret**，Firebase 官方文档明确说明 web apiKey 是前端可见的公开标识符，用于把 SDK 请求路由到 Firebase 项目，并非授权凭证。真正的鉴权由 Firebase Security Rules 与 App Check 在服务端完成。

参考：https://firebase.google.com/docs/projects/api-keys#apikey-is-not-secret
> Note: Despite the word "key" in the name, API keys for Firebase services are not used to control access to backend resources; that can only be done with Firebase Security Rules and App Check.

**为什么硬编码**：该 apiKey 是 Windsurf 官方 `exa2-fb170` 项目的公开值，从 `https://windsurf.com/_next/static/chunks/46097-*.js` 中提取。Windsurf 自家 web 客户端和 IDE 扩展都用同一个值。测试过另外 3 个看起来是"备用" key，均 401 失败 —— 该值不可轮换。

**处理**：在 CNB 安全面板将此 3 条规则标为 **忽略此风险**。

### 2. `@ant-design/colors@6.0.0` 被匹配到 CVE-2021-23567

**位置**：`web/pnpm-lock.yaml`

**结论**：**张冠李戴**。CVE-2021-23567（Marak Squires 在 2022 年 1 月把自己的 `colors` 包里塞了无限循环的自毁事件）影响的是 npm 上的 `colors`（无 scope），与 `@ant-design/colors`（Ant Design Team 维护、用于设计系统色板运算）没有任何关系。scanner 按名字前缀匹配产生的误报。

验证：
```bash
$ grep -nE '^  colors[@:]' web/pnpm-lock.yaml
# （空 —— 本仓库完全没有 Marak 的那个 colors 包）
```

`@ant-design/colors@6.0.0` 只依赖 `@ctrl/tinycolor@3.6.1`，两者都是健康的。

**处理**：在 CNB 安全面板将此条规则标为 **忽略此风险**。

## 真实漏洞修复记录

### CVE-2025-22870 — golang.org/x/net HTTP Proxy bypass via IPv6 Zone IDs

- **受影响**：`golang.org/x/net < 0.36.0`
- **本仓库状态**：已在 commit `<本次提交>` 升级到 `v0.36.0`
- **风险**：`golang.org/x/net/proxy` 和 `golang.org/x/net/http/httpproxy` 在解析带 IPv6 Zone ID 的 URL 时错误处理 proxy 白/黑名单，可能绕过 `NO_PROXY` 匹配
- **本服务受影响程度**：**低** — 服务内部代理仅用于 LS 和 Firebase 的出站连接，由 [proxycfg](internal/proxycfg) 显式拼装 URL，不经过用户输入
- **仍修复的原因**：遵循最小惊讶原则，保持依赖栈干净，且升级对行为没有可观测影响

## 固有安全特性

- 控制台密码、API Key 在日志中脱敏（只保留前 8 位）
- 代理测试内置 SSRF 防护：拒绝 `127.0.0.0/8` / RFC1918 / `169.254/16` / CGNAT (`100.64/10`) / ULA (`fc00::/7`) / `metadata.*` 等私有或云元数据地址
- gRPC 单次 / 流帧大小 64 MB 硬上限
- `accounts.json` / `proxy.json` 等敏感文件原子写（`tmp+rename`，`0o600` 权限）
- LS CSRF token 每次启动随机（64 位 `[A-Za-z0-9]`），历史硬编码值 `windsurf-api-csrf-fixed-token` 被拒
- CORS `CORS_ALLOWED_ORIGINS` 精确回显模式
- 第三方运行时依赖零生态：仅 `golang.org/x/net/http2` 及传递依赖 `golang.org/x/text`
- `/dashboard/api/*` 所有写操作请求体 1 MB 上限，`/v1/*` 32 MB
- 服务退出时 30 秒 graceful drain，断开前完成在途请求

## 建议的部署安全基线

参考 [docs/DEPLOY.md](docs/DEPLOY.md) systemd 单元：

```
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/windsurfapi /opt/windsurf /tmp/windsurf-workspace
```

专用用户 `windsurfapi:windsurfapi`，`.env` 文件 `chmod 600`，不对外暴露 `:42100`（Language Server 本地端口，控制台配置里的 CSRF token 足以防同机其他用户接入）。
