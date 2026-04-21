# 变更日志

所有有意义的变更都会记录在本文件。版本采用 [语义化版本](https://semver.org/lang/zh-CN/)。

## [1.2.1-go] — 2026-04-21

安全与依赖维护版本。

### 安全

- 升级 `golang.org/x/net` `v0.34.0 → v0.36.0` 修复 CVE-2025-22870（HTTP Proxy bypass via IPv6 Zone IDs）。本服务的代理链路由 `proxycfg` 内部显式拼装，不走用户输入，真实影响面低，但仍按最小惊讶原则随上游升级
- 新增 [SECURITY.md](SECURITY.md) 说明漏洞披露渠道、扫描器两条误报（Firebase 公开 apiKey、`@ant-design/colors` 被字符串匹配误认成 Marak 的 `colors`）、以及固有安全特性清单

### 其它

- `go.mod` 的 `go` directive 升到 `1.26.2`，与本机工具链对齐
- 依赖 `golang.org/x/text` 传递升级 `v0.21.0 → v0.22.0`

## [1.2.0-go] — 2026-04-21

首个提交 CNB 的版本。相对于 JS 原版的完整功能等价，外加若干运维侧改进。

### 新增

- **完整重写**：Go 1.22+ 单二进制，约 9 MB 静态产物
- **内嵌 Vue 3 控制台**：通过 `//go:embed all:dist` 打入二进制，1.1 MB 左右
- 9 个控制台页面：仪表盘 / 统计分析 / 登录取号 / 账号管理 / 异常监测 / 模型控制 / 代理配置 / 运行日志 / 实验性功能
- Ant Design Vue 4 + Pinia + vue-router + 统一 axios 请求封装
- 响应式布局：桌面侧边栏、窄屏自动切抽屉，适配手机 / iPad 竖屏 / 横屏
- 日间模式纯白主题（应用户要求，无暗色模式）
- 限速 / 限流状态**持久化**到 `accounts.json`，重启不丢
- 限速时长**自动解析**上游错误里的 `27m31s` / `retry after 30 seconds` / `retry_after: 30` 等格式
- 限速窗口计算规则：`ceil(服务器报的时长, 1min) + 1min 缓冲`（例：`5m → 6m`、`27m31s → 29m`）
- 账号停用 / 启用功能（保留配置但移出轮询）
- 模型身份注入（`modelIdentityPrompt`）、Cascade 对话复用等实验性开关
- `/accounts` 响应体去重：`tierModels` 改为顶层共享索引，每行只留计数，36 账号响应从 150+ KB 瘦到 35 KB
- OpenAI `tools[]` 与 Cascade 文本协议互转的 toolemu 模块
- 内置响应缓存（按请求体精确哈希）
- SSE 实时日志面板（ring buffer + 广播）
- 自更新：`git pull` + 有序退出，由 systemd / PM2 等拉起
- 代理测试：实际发起 HTTP CONNECT，返回出口 IP + 延迟；支持在主机字段直接贴 `host:port`

### 改进

- Language Server 停止时**等端口真正释放**再返回，修复 StopAll 后立即 Ensure 撞到自家 dying LS 的"幻影 Entry"问题
- `StopAll` / `isRateLimited` 等所有 Account 字段访问受 `Pool.mu` 保护
- LS stderr 里的 `panel state not found` / `path is already tracked` / `Got signal terminated` 等预期恢复/关停信号降级到 DEBUG，不再占 ERROR/WARN
- `AddTrackedWorkspace` 的"已跟踪"软错误不再以 WARN 打印
- 账号余额列直接展示 **日 / 周两个百分比**（"剩余"语义）并按剩余量着色（≤10% 红、≤30% 橙）
- 异常监测页展示**全部账号**并按严重度排序，非"只显示异常"
- 状态标签中文化：`active → 正常` / `error → 错误` / `expired → 已过期` / `disabled → 已停用` 等
- Firebase OAuth 在非 `localhost` / `windsurf.com` 域名下自动禁用按钮并给出清晰指引
- 登录取号失败历史也会写入本地记录（此前只有成功的才入库）
- 统计分析页柱图对齐 Go Snapshot 的真实字段（`hourlyBuckets` / `modelCounts` map）
- 模型控制页的访问策略 / 清单编辑，空白名单时后端返回 `[]` 而非 `null`，避免前端 `.length` 崩溃
- 运行日志容器高度改成 `calc(100vh - 200px)`，填满剩余视口
- 代理测试字段名与后端对齐（`username/password/egressIp/latencyMs`）
- 登录取号的历史列表兜底时区 `Asia/Shanghai`，不跟浏览器时区混

### 安全

- 控制台密码、API Key 经过日志脱敏（只保留前 8 位）
- 代理测试 SSRF 防护：拒绝 127.0.0.0/8、RFC1918、169.254、CGNAT、ULA、metadata.* 等私有地址
- gRPC unary / stream 帧大小 64 MB 上限
- 账号池等持久化文件原子写（`tmp+rename`，`0o600` 权限）
- 依赖零生态：除 `golang.org/x/net/http2` 及其传递依赖 `golang.org/x/text` 外无第三方
- LS CSRF token 每次启动随机（64 位 `[A-Za-z0-9]`），历史固定 token 不可复用
- CORS `CORS_ALLOWED_ORIGINS` 精确回显模式，避免通配符泄露

### 已知限制

- `modelIdentityPrompt` 对 `grok-*` 无效，grok 的 RLHF 偏见压不住"我是 Cascade"的响应
- Firebase Auth 只接受 `localhost` / `windsurf.com` 作为来源，自建 IP 部署走 SSH 隧道即可
- 自更新只对 git clone 部署生效；SFTP / tar 部署需手动替换二进制
