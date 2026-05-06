# CHANGELOG

本文件记录 Go 版本反代（`windsurfapi`）的版本变更。遵循 [Keep a Changelog](https://keepachangelog.com/)
与 [Semantic Versioning](https://semver.org/) 约定，日期为 UTC。

---

## [1.4.17] - 2026-05-06

### Node ↔ Go 补丁 backport — 第二批 7 项

把 1.4.16 留作"独立 PR"的 6 项 + 衍生 1 项全部落地。叠加 1.4.16 的 13 项,
38 项 Node 1.4.0 补丁现在 Go 端已实施 20 项 / 已具备 14 项 / N/A 4 项 = 全
覆盖。

### 网络安全 (P0/P1)

- **N3 cloud/transport SSRF 守卫**：抽 `internal/netguard/` 新包
  (`IsPrivateIP` / `IsPrivateHost` / `ResolveAndCheckHost` / `CheckProxyURL`)
  作为整个项目的私网/loopback/metadata-service 拒绝点。
  [`cloud/transport.go:clientFor`](internal/cloud/transport.go) 在
  创建 `http.Transport` 之前调 `netguard.ResolveAndCheckHost(host)`,
  proxy 主机解析到私网即拒绝并 fallback 到直连出口(同时打 warn)。
  也涵盖 metadata service 主机名(metadata.google.internal /
  metadata.aws.internal / 169.254.169.254 / 100.100.100.200)以及
  `.local` / `.internal` / `.lan` / `.home` 后缀。
- **N22 SOCKS5 代理支持**：`langserver/pool.go` 的 `Type: "socks"
  // not yet honoured` 标注 deprecated。`cloud/transport.go` 现在用
  `golang.org/x/net/proxy.SOCKS5` 实现 SOCKS5 出口,带 user/pass
  RFC1929 认证。SOCKS5 dialer 包了一层 `guardedDialer`,即便
  SOCKS 服务器试图骗 dialer 连内网地址(`Dial("169.254.169.254:80")`)
  也会在 TCP 打开前被 `netguard.ResolveAndCheckHost` 拦下。
- **B-P1-3 SOCKS SSRF**(N22 同步):由 `guardedDialer` 提供。

### 协议兼容 / 弹性 (P1/P2)

- **N13 + N23 NLU intent-extractor**：[`toolemu/intent.go`](internal/toolemu/intent.go)
  3 层意图识别 + 工具索引(规范名 / primary required string param)。
  - Layer 1 explicit syntax — `func_name(arg=X)` / `function_call:
    func_name(...)` 显式调用形式,置信度 0.9。
  - Layer 2 backtick-quoted —``` `tool` with `arg` ``` 反引号成对模式,
    置信度 0.7。
  - Layer 3 narrative — "I should call X function with the command 'Y'"
    自然语言陈述,需用户消息含 action 动词(`run`/`list`/`read`/...)才
    放行,置信度 0.5。
  `WINDSURFAPI_NLU_RECOVERY=0` 关闭整层。
  - [`server/chat.go:runOnce`](internal/server/chat.go) 在 `parseAll`
    返回 0 个 tool_calls 且声明了 tools[] 时调 `ExtractIntent`,把恢复
    出的工具调用作为正常 ToolCall 上抛 — GLM-4.7 / GLM-5.x / Kimi /
    Qwen 现在能在 cascade 后端可靠完成工具调用。
  - 新增 `streamInput.Tools` 字段贯穿调用链,`lastUserMessageText`
    helper 取最后一条 user 消息文本作为 Layer-3 gate。
- **N24 Cascade native tool bridge**：[`toolemu/bridge.go`](internal/toolemu/bridge.go)
  把 OpenAI 工具(Read / Bash / Glob / Grep / Write / Edit /
  WebFetch + 各种别名)正向翻译到 Cascade native step kinds
  (view_file / run_command / find / grep_search_v2 / list_directory /
  edit_file / browser_open)。`CanUseNativeBridge` 是入口闸门 — 全部
  工具能映射时才启用 bridge,部分映射会 fallback 到原 prompt
  emulation 路径(避免混合方言把模型搞糊涂)。`BuildReverseLookup` /
  `CascadeStepToOpenAIToolCall` 提供反向通道。

### 体验 (P2)

- **N19 i18n 集中化**：源 of truth 在 [`internal/i18n/`](internal/i18n/)
  (`zh-CN.json` + `en.json`),通过 `//go:embed` 嵌进二进制。
  `GET /dashboard/api/i18n` 返回 locale 列表;
  `GET /dashboard/api/i18n/:locale` 返回该 locale JSON
  (Cache-Control: public, max-age=300)。Vue SPA 在
  [`web/src/composables/useI18n.ts`](web/src/composables/useI18n.ts)
  提供轻量 composable(`t(key, params)` / `setLocale` /
  `loadFromBackend`),浏览器语言自动探测,localStorage 持久化用户选择。
  [`web/src/i18n/`](web/src/i18n/) 是构建期 fallback,与
  `internal/i18n/` 保持同步。Vite 直接 `import zhCN from
  '../i18n/zh-CN.json'` 编进 bundle,与运行期 fetch 互为兜底。
- **N20 local-windsurf 凭证导入**：[`dashapi/local_windsurf.go`](internal/dashapi/local_windsurf.go)
  扫本机 Windsurf 桌面安装的 `state.vscdb` 路径(macOS / Linux /
  Windows / Snap;stable + Next + Insiders 三个 flavor)。
  `GET /dashboard/api/local-windsurf` 返回候选路径列表 +
  sqlite3 CLI 是否可用。
  `POST /dashboard/api/local-windsurf/import {"path": "..."}` 通过
  `sqlite3 -readonly` shell-out 读 `ItemTable WHERE key LIKE
  '%windsurfAuthStatus%'` 抽 apiKey + email,5 秒超时,3 个 flavor
  自动分类。无 sqlite3 CLI 时返回 manual hint("Copy state.vscdb out
  and POST to /accounts manually")而非硬错。**不引入 CGo SQLite 依赖**
  保持单二进制 ~13 MB。

### 涉及文件

- `internal/netguard/netguard.go` (新增,SSRF 守卫共享包)
- `internal/cloud/transport.go` (N3 + N22 + guardedDialer)
- `internal/toolemu/intent.go` (新增,N13/N23 NLU 恢复)
- `internal/toolemu/bridge.go` (新增,N24 cascade native bridge)
- `internal/server/chat.go` (NLU 接入 + Tools 字段 + lastUserMessageText)
- `internal/i18n/embed.go` (新增,N19 后端 i18n 源)
- `internal/i18n/{zh-CN,en}.json` (新增)
- `internal/dashapi/dashapi.go` (i18n + local-windsurf 路由)
- `internal/dashapi/local_windsurf.go` (新增,N20)
- `web/src/composables/useI18n.ts` (新增,N19 SPA composable)
- `web/src/i18n/{zh-CN,en}.json` (新增,SPA 构建期 fallback)

### 已知后续工作(本批未涉及,留作单独 PR)

- **SPA 视图字串实际外提**:目前 `composables/useI18n.ts` 已就位,
  但各个 .vue 视图里的中文字串还是写死的。后续重构需把
  `<h1>账号管理</h1>` 之类全部改为 `<h1>{{ t('accounts.title') }}</h1>`,
  并把 `Accounts.vue` / `Models.vue` / `Proxy.vue` / `Stats.vue` 等所有
  view 过一遍。这是机械工作,但跨 ~20 个文件,适合独立 PR。
- **dist/ 重新构建**:本批改动了 `web/src/`,生效需要
  `pnpm --dir go/web build` 后重新 commit `internal/web/dist/`。
- **NLU 实战调校**:`intent.go` 的 regex 是首版,等 GLM-5.2 / Kimi-K3
  上线后需要按真实 narrative 模式扩 patterns。

---

## [1.4.16] - 2026-05-06

### Node ↔ Go 补丁 backport — 13 项实施 / 14 项已实现 / 4 N/A / 7 后续

把 Node `src/` 1.4.0 的 P0/P1/P2 + Agent Team 安全 sweep 共 38 项,经 gap
analysis 后映射到 Go 实现。已完成 13 项,先前版本已实现 14 项,4 项 N/A
(Go 概念上不适用如 prototype pollution),剩 7 项是新增大模块保留作后续。

### 安全加固 (P0)

- **N4 bindHost 自动锁回环**：`config/config.go` `Load()` 检测到
  `API_KEY=""` 且 `DASHBOARD_PASSWORD=""` 且 `ALLOW_OPEN!=1` 时,默认
  `BindHost` 从 `0.0.0.0` 降到 `127.0.0.1`。`main.go` 启动后调
  `emitNoAuthWarnings(cfg)`,公网 bind 但无凭据时打 fat banner。
- **N1 cache CallerScope**：`cache/cache.go` `RequestBody.CallerScope`
  字段 + `Key()` 用 `\x00` 分隔符把 scope 折入 digest。`server/chat.go`
  新增 `callerScopeFor(r, body)`:apiKey hash + `body.user / conversation /
  previous_response_id / metadata.{conversation_id, session_id, user_id}`,
  fallback IP+UA。修共享 apiKey 多租户场景两客户端互见缓存的串响应。
- **N14 Claude Code 2.x device_id**:`bodyCallerSubKey` 解析
  `metadata.user_id` JSON-encoded `{device_id, account_uuid, session_id}`,
  device_id 进入 callerScope。`cmd/main.go` 启动时在
  `/tmp/windsurf-workspace/.windsurf-proxy-workspace` 落 marker 文件,
  Cascade 工具提示不再 narrate "the workspace appears empty"。
- **N5 sanitize 跨平台 + XML 块剥离**：`sanitize/sanitize.go` patterns
  增加 Windows 反斜杠路径覆盖（`(?:[A-Za-z]:)?[/\\]home[/\\]user…`)
  + `(?s)<workspace_information>…</workspace_information>` 等 XML 块
  strip,Cascade 上游注入的 sandbox 元数据不再 echo 给客户端。workspace
  替换标记从 `.` 改为 `<workspace>`,避免相对路径被工具调用重新解读为
  Read 目标导致循环。
- **N6 Cache-Control: no-store**：`server/server.go` `noCompress` 中间
  件把 `Cache-Control` 从 `no-cache` 升到 `no-store`,防 sub2api / nginx
  priority cache 把一个用户的响应误返给另一个。
- **N9 fresh-account 403 race**：`models/catalog.go` `IsTierAllowed` 把
  `tier="unknown"` 视同 pro 路由,新加账号到探测完成的窗口期不再 403
  全部 premium 模型。

### Agent Team OWASP

- **B-P2-4 + F-P2-1 + F-P2-2 dashboard 安全头**：`server/server.go`
  `writeSPAIndex` 新增 `X-Frame-Options: DENY`、`X-Content-Type-Options:
  nosniff`、`Referrer-Policy: same-origin` 与完整 CSP（`default-src
  'self'` / `frame-ancestors 'none'` / `object-src 'none'` / `base-uri
  'none'`）。
- **F-P1-3 sessionStorage 改造**：`web/src/api/request.ts` dashboard
  密码从 `localStorage` 移到 `sessionStorage`,带一次性 legacy 迁移
  逻辑。XSS 一旦发生密码暴露窗口从永久变为单次浏览会话。
- **N27 brute-force lockout**：`dashapi/dashapi.go` per-IP 失败计数,
  5 次/30 分钟封禁,锁定检查在 password compare 之前(快比对器无法
  绕过)。空闲条目 2 小时后清理。

### 弹性 (P1)

- **N10 模型目录扩展**：`models/seed.go` 补 GPT-5.5 全 11 档(`gpt-5.5
  -{none,low,medium,high,xhigh}` + `*-fast`)+ GPT-5.3-codex 完整 tier
  ladder + `glm-4.7-fast` + `adaptive`(deprecated)。`catalog.go` 新增
  Anthropic SDK dotted-form 别名(`claude-haiku-4.5` / `claude-sonnet-4.5`
  / `claude-opus-4.5` 含 dashed/`-latest`/带日期形式)。
- **N15 claude-opus-4-7-thinking 别名**:bare `-thinking` 不再自动路由
  到高 effort,统一降到 medium 档,客户端要其它档需传完整名。
- **N11 自动 model fallback**：`models/catalog.go` 新增 `fallbackChain`
  表 + `FallbackFor()`。chat.go 在所有账号都被 rate-limited 时,先按链
  表降一档(opus-xhigh→high→medium…→sonnet→haiku→flash)再上 429。
  `streamInput.FallbackHop` 限制只跳一次,避免静默连降三档。流式入口
  做 `IsAllRateLimited` 预检 + 在写 SSE header 前做 fallback。
- **N18 drought mode**：`auth/pool.go` 新增 `IsDroughtMode()` /
  `IsModelBlockedByDrought()` / `GetDroughtSummary()`。所有有 quota 数据
  的活跃账号 < 5% 周配额时,premium 模型立刻 503;free-tier-shared 模型
  (gpt-4o-mini / gemini-2.5-flash / glm-4.7 / kimi-k2 / qwen-3)继续放行。
- **N16 probe singleflight**：`auth/ops.go` `Probe()` 加 `probesInFlight`
  map。dashboard 手动探测 + 调度器 + 加账号自动探测同时发起对同一 id
  时,共享同一次探测结果而非各自烧 RPM。

### 可观测性 (P2)

- **N25/N26 circuit breaker stats**：`stats/stats.go` `AccountCounts` 新
  增 `RateLimitEvents` / `InternalErrorEvents` / `QuarantineEvents` /
  `FallbackEvents` 计数 + `LastEventAt`。`RecordCircuitEvent(accountID,
  kind)` 让 chat.go 在 `MarkRateLimited` / `ReportInternalError` 路径
  记一次,dashboard `/dashboard/api/stats` 可呈现每账号可靠性曲线。

### 已通过先前版本实现 (gap analysis 验证)

`N2 fs-atomic`(atomicfile 包) / `N7 apiKey 屏蔽`(`AuthAccounts` 已
mask)/ `N8 varint BigInt`(Go uint64 原生)/ `N12 /v1/responses`
(1.4.0-go)/ `N17 LS orphan cleanup`(`langserver/kill_linux.go`)/
`N21 image/pdf`(`imagex` 包)/ `B-P1-1 git command injection`
(`isSafeBranchName`)/ `B-P1-2 timing-safe compare`
(`subtle.ConstantTimeCompare`)/ `B-P2-3 TLS InsecureSkipVerify`
(cloud/transport TLS 配置无 Skip)/ `F-P1-1+F-P1-2 XSS`(Vue 模板
自动转义)。

### N/A (Go 概念上不适用)

`B-P2-1 prototype pollution` / Node 特定的 `BigInt varint` 隐患 /
F-P1-* 中已被 Vue 自动转义的项。

### 后续待办 (大型新增模块,留独立 PR)

- **N3 cloud/transport SSRF**:抽 `internal/netguard` 包,在
  `cloud/transport.go:clientFor` 的 proxy 设置前接入,与 dashapi 共用。
- **N13 + N23 NLU intent-extractor**:391 行 3 层意图识别,从 GLM/Kimi
  自然语言 narrative 恢复 tool_call。
- **N22 SOCKS5**:`langserver/pool.go` 标 `// not yet honoured`,需要在
  cloud/transport.go 接入 `golang.org/x/net/proxy.SOCKS5` 自定义 dialer
  + N3 的 SSRF 守卫。
- **N24 cascade-native-bridge**:把 OpenAI 工具(Read/Bash/Glob/Grep)
  正向翻译到 Cascade vocabulary(view_file/run_command/find/
  grep_search_v2),trajectory 反向翻译。
- **N19 i18n centralised**:Vue SPA 内嵌字串外提到 `web/src/i18n/*.json`。
- **N20 local-windsurf 凭证导入**:扫本机 Windsurf 桌面安装的
  `state.vscdb` 抽取缓存凭据。

### 涉及文件

- `internal/config/config.go`、`cmd/windsurfapi/main.go`(N4 + N14 marker + emitNoAuthWarnings)
- `internal/cache/cache.go`、`internal/server/chat.go`(N1 + N11 + N18 + N14 device_id)
- `internal/server/server.go`(N6 + B-P2-4)
- `internal/sanitize/sanitize.go`(N5)
- `internal/models/seed.go`、`internal/models/catalog.go`(N9 + N10 + N11 fallback + N15)
- `internal/auth/pool.go`(N18 drought)、`internal/auth/ops.go`(N16 singleflight)
- `internal/dashapi/dashapi.go`(N27)
- `internal/stats/stats.go`(N25/N26)
- `web/src/api/request.ts`(F-P1-3)

---

## [1.4.15] - 2026-04-23

### 新增
- **Claude Opus 4.7 全系 5 档 seed 条目**：对照生产云端目录，补齐
  `claude-opus-4.7-{low,medium,high,xhigh,max}`，pricing.go 同步
  5 条 USD/M token 价（15/75，Anthropic Opus 公价）。之前 pricing 有
  两条 hyphen-style 孤儿条目（`claude-opus-4-7` / `-max`），删掉避免
  混淆——真实统计走 `info.Name`（dot-style）查价，hyphen key 永远
  命中不到。

### 下线
- **BYOK 4 个账单层变体**：`model-claude-4-opus-byok` /
  `-opus-thinking-byok` / `-sonnet-byok` / `-sonnet-thinking-byok`。
  上游把"Bring-Your-Own-Key"走的请求标成独立 modelUid 做账单分流，
  但本反代池用的就是 Windsurf 的共享账号池——对外暴露这 4 个只会
  让用户在模型选择器里看到近似重复项，误选后路由到同一个底层模型。
  `models.MergeCloud` 里 hardcode 过滤 `*-byok` 后缀，不走 model-access
  blocklist（避免 blocklist 被清空后又冒出来）。

### 涉及文件
- `internal/models/seed.go`：5 条 Opus 4.7 seed entries。
- `internal/models/pricing.go`：5 条 Opus 4.7 定价；删 2 条 hyphen 孤儿。
- `internal/models/catalog.go`：`MergeCloud` 追加 BYOK 过滤（`strings.HasSuffix(..., "-byok")`）。

---

## [1.4.14] - 2026-04-23

### 修复（异常监测漏记）
- **Capability Probe 的 rate-limit 分支不写 banhistory**：
  [`auth/ops.go`](internal/auth/ops.go) 的 `Probe` 在 canary 命中 rate-limit
  时之前只打 `logx.Info`、直接 `continue`，不调 `MarkRateLimited` —
  banhistory 没记录，dashboard "异常监测 / 历史记录"漏掉了 6 小时周期探测
  发现的所有限速事件。改为命中时 `p.MarkRateLimited(apiKey, 5m, model)`
  + `logx.Warn`。
- **`reRateLimit` 正则覆盖不全**：旧正则只认 `rate limit` / `rate_limit` /
  `too many requests` / `quota` 四种措辞。Cascade 也会返回 `daily limit
  reached` / `message limit exceeded` / `usage limit hit` / `retry-after`
  等形式，旧分类漏判 → 走 `isModel` 分支 → 只 `UpdateCapability("model_error")`
  不 `MarkRateLimited` → banhistory 不记。新正则扩到 8 类措辞，Probe 和
  `classify` 通过 `auth.IsRateLimitMessage` 共享同一规则，不会再分叉。

### 新增
- `auth.IsRateLimitError(err) bool` / `auth.IsRateLimitMessage(msg string) bool`
  — 单一判定入口，外部包可调用。
- `internal/auth/ops_test.go` 29 条单测覆盖 17 种已知 rate-limit 措辞 +
  10 种不应误判的错误（auth/precondition/context-canceled/transport 等）。

### 涉及文件
- `internal/auth/ops.go`：`rateLimitRE`、`IsRateLimitError`、`IsRateLimitMessage`、
  Probe rate-limit 分支从 log-only 改为 MarkRateLimited。
- `internal/server/chat.go`：`reRateLimit` 删除，`classify` 改用
  `auth.IsRateLimitMessage`。
- `internal/auth/ops_test.go`：新增。

---

## [1.4.13] - 2026-04-23

### 修复
- **Anthropic SSE 事件顺序**：`emitTextDelta` / `emitThinkingDelta` /
  `emitToolCallDelta` 入口都强制先 `startMessage()`，保证
  `message_start` 永远是流的第一个事件。早期错误路径（如 `streamShim.drain`
  把上游 HTTP 错误转 text_delta）之前会先发 `content_block_start`，
  部分 SDK 会拒绝这种顺序。
- **SSE 出站 JSON 校验**：`send()` 在 `json.Marshal` 之后加一次
  `json.Unmarshal` 回路验证；若产生的字节客户端 parse 不回来就丢帧 +
  `logx.Warn`，不再把非法 JSON 写给客户端。原实现忽略了 `json.Marshal`
  的 error（`_, _ :=`），异常情况下会 `data: \n\n` 写空体触发客户端
  `SyntaxError`。

### 新增
- 出站 SSE 诊断日志：`sse → event=... data=...` 的 `logx.Debug` 行，
  `journalctl -u windsurfapi | grep 'sse →'` 可直接看到每个发给客户端
  的 Anthropic 事件内容（前 200 字节）。排查 `Unexpected identifier
  "Error"` 这类未复现的客户端解析错误时用。

### 涉及文件
- `internal/server/messages.go`：`send`、`emitTextDelta`、
  `emitThinkingDelta`、`emitToolCallDelta`。

---

## [1.4.12] - 2026-04-23

### 新增
- **默认简体中文响应**：新增可配置项 `responseLanguagePrompt`，注入
  Cascade 的 `communication_section`（field 13），让所有模型默认以简体中文
  回答，用户改用其他语言时自动跟随。不经过 identity 注入路径，不会触发
  Claude Code / Cursor / OpenCode 的 prompt-injection detector。
- 运行时从 `runtimecfg.GetResponseLanguagePrompt()` 读取；写 `" "`（单空
  格）到 `runtime-config.json` 可关闭。

### 涉及文件
- `internal/runtimecfg/runtimecfg.go`：`DefaultResponseLanguagePrompt`、
  `ResponseLanguagePrompt` 字段、`GetResponseLanguagePrompt()`。
- `internal/client/client.go`：`CascadeOptions.ResponseLanguagePrompt`。
- `internal/windsurf/windsurf.go`：`SendOpts.ResponseLanguagePrompt` →
  `communication_section` 末尾追加。
- `internal/server/chat.go`：`runOnce` 与流式路径均从 `runtimecfg` 读取。

---

## [1.4.11] - 2026-04-23

### 变更
- 恢复 10 个厂商的默认 identity-prompt 模板（anthropic / openai / google
  / deepseek / xai / alibaba / moonshot / zhipu / minimax / windsurf），
  `modelIdentityPrompt` 默认改回 `true`。dashboard 可逐条编辑或清空。
- README 加入"Cascade anti-injection 误报"的已知副作用说明，并指出解决
  方法（清空触发 provider 的模板或关闭 flag）。

---

## [1.4.10] - 2026-04-23

### 变更
- 仅为 `anthropic` 保留克制版默认 identity 模板，其他厂商留空。文案避
  开 injection-detector 触发词（`ignore` / `override` / `NEVER` /
  `CRITICAL`），避免 Claude Code 静默丢弃 assistant turn。

---

## [1.4.9] - 2026-04-23

### 修复（关键安全修复）
- **跨客户端上下文串线**：[`convpool`](internal/convpool/pool.go)
  的 `FingerprintBefore` / `FingerprintAfter` 之前**只对消息历史做
  SHA-256**，完全没有调用者维度隔离。当两个不同客户端发送了消息序列
  一致的请求（Claude Code 固定 system prompt + CLAUDE.md 模板撞
  指纹的常见场景），`cascadeConversationReuse` 开启时会把对方的
  `cascade_id` Checkout 回来，Cascade 后端沿用旧 session state，导致
  A 的对话历史被注入到 B 当前 turn。表现为"模型一会儿讨论别的项
  目"、"中英文乱跳"、"agent 误以为在别的 repo"。
- 修复方式：两个 fingerprint 函数加入 `clientSalt` 参数，算法改为
  `sha256(salt + "\x00" + messages)`。`chat.go` 新增 `clientIPSalt(r)`
  按 `X-Forwarded-For` → `X-Real-IP` → `RemoteAddr` 提取 salt，`streamInput`
  多一个 `ClientSalt` 字段贯穿流式 / 非流式两条路径。
- 回归测试（`internal/convpool/pool_test.go`）4 条：salt 不同则指纹
  不同、salt 相同则指纹一致、before/after 契约对等、空 salt 兼容。

### 边界说明
- 同一 NAT 出口 IP 下多用户仍可能共享 salt → 指纹相撞。要彻底根治需
  要客户端显式传 `x-conversation-id`。1.4.9 已将误命中从"任意两客户端
  消息相同"缩小到"同一 NAT 下两客户端消息相同"。

---

## [1.4.8] - 2026-04-23

### 修复
- **`SyntaxError: Unexpected identifier "Error"`（Claude Code 侧）**：
  当 Cascade 偶发生成的 tool_call body 将 `arguments` 设为一个**字符串
  值**（而非 JSON 对象）且内容不是合法 JSON（例如 `"Error: permission
  denied"`），旧版会原样透传到 Anthropic SSE 的 `input_json_delta.partial_json`，
  导致 Claude Code 在 `content_block_stop` 时 `JSON.parse` 炸掉整个
  assistant turn。
- 新增 `normalizeToolArguments(raw)`：强制 `ArgumentsJSON` 必为合法
  JSON object 字符串，非法形状一律降级为 `{}`（由 Claude Code 的 tool
  schema 报"缺必填参数"，至少不会炸流）。
- 回归测试增加 5 条，再加一条 invariant：任意输入只要解析成功，返回
  的 `ArgumentsJSON` 必能 `json.Unmarshal` 成 `map[string]any`。

---

## [1.4.7] - 2026-04-23

### 变更
- `modelIdentityPrompt` 默认关闭，`DefaultIdentityPrompts` 清空
  （后在 1.4.10/1.4.11 按用户反馈分步恢复）。
- Dashboard 实验面板 toggle 初值同步为 `false`。

### 说明
- 该版本目标是"默认不做身份伪装"。后来反馈显示部分用户需要继续用身
  份注入（走 claude-* 模型），1.4.10 / 1.4.11 重新上线并加上 "anti-injection
  副作用"告警。

---

## [1.4.6] - 2026-04-23

### 修复
- **"The model produced an invalid tool call"**：
  `server/chat.go` 的两处 `sanitize.Text(tc.ArgumentsJSON)` 会把工
  具参数里的 `/tmp/windsurf-workspace/foo.py` 替换成 `./foo.py`。
  Claude Code 的 `Read` / `Write` / `Edit` 工具 schema 要求 `file_path`
  是 **absolute path**，相对路径会被 schema 校验拒收。
- 移除对 tool arguments 的 sanitize（它是协议合约，不是 prose）；
  普通文本、思考流、错误消息的 sanitize 保留不变。
- 同时在 `emitTool` 添加 `logx.Debug` 记录每次下发的工具名与前 200
  字节 args，便于后续同类问题排查。

---

## [1.4.5] - 2026-04-23

### 修复
- **工具调用失败（Claude Code 的 Write / Read / Bash）**：
  Cascade 生成带多行 `content` 参数的 tool_call 时经常吐出**未转义
  的字面 `\n` / `\t`**，旧版 `parseToolCallBody` 用严格 `json.Unmarshal`
  解析，直接失败，`<tool_call>` 块被当作普通文本回给 Claude Code —
  客户端看到裸 XML 标签，不触发任何 tool_use 事件。
- 新增回退路径：strict 解析失败后尝试 `repairLLMJSON`（剥离 markdown
  代码围栏、在字符串字面量内部转义 `\n \r \t \b \f` 等控制字节），
  再做一次 strict 解析。
- 单元测试 9 条覆盖：单行 JSON / 字面换行 / 字面 tab / markdown 围栏
  / 围栏+换行 / pretty-printed / 不二次转义 / 真坏 JSON 不放过 / 端
  到端流式解析。

---

## [1.4.4] 及以前

见 git log。主要包括：

- `#3` 修复邮箱+密码登录失败后没有给出 OAuth 提示的引导文案。
- `#8` 追加 Pro trial 分级手工覆盖，修正错认为 free 导致的模型不可用。
- `/self-update` 用 `process.exit` + PM2 autorestart 代替 spawn 避免
  Windows SFTP 热更后子进程孤儿化。
- 1.3.x：引入 `/v1/messages`（Anthropic 兼容）、legacy `RawGetChatMessage`
  proto 编码兼容、账号冷启动 180s 超时上限、dashboard "立即更新"按钮。
- 1.2.x：Cascade `planner_mode = NO_TOOL (3)`、Firebase token 刷新
  周期、LS pool per-proxy 路由、per-account rate limiting、82 模型
  目录 + cloud API 合并。

---

[1.4.12]: https://github.com/dwgx/WindsurfAPI/tree/master/go
[1.4.11]: https://github.com/dwgx/WindsurfAPI/tree/master/go
[1.4.10]: https://github.com/dwgx/WindsurfAPI/tree/master/go
[1.4.9]: https://github.com/dwgx/WindsurfAPI/tree/master/go
[1.4.8]: https://github.com/dwgx/WindsurfAPI/tree/master/go
[1.4.7]: https://github.com/dwgx/WindsurfAPI/tree/master/go
[1.4.6]: https://github.com/dwgx/WindsurfAPI/tree/master/go
[1.4.5]: https://github.com/dwgx/WindsurfAPI/tree/master/go
