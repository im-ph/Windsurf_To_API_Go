# CHANGELOG

本文件记录 Go 版本反代（`windsurfapi`）的版本变更。遵循 [Keep a Changelog](https://keepachangelog.com/)
与 [Semantic Versioning](https://semver.org/) 约定，日期为 UTC。

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
