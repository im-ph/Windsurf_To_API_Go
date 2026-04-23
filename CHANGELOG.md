# 变更日志

所有有意义的变更都会记录在本文件。版本采用 [语义化版本](https://semver.org/lang/zh-CN/)。

## [1.3.8-go] — 2026-04-22

修复 Claude Code 通过 `/v1/messages` 走反代时"**上下文消失 + 返回空白**"的三个根因。

### 症状定位

Claude Code 设 `ANTHROPIC_BASE_URL=<反代>/v1` 后：
- **返回空白**：某些请求 assistant 气泡完全空白，无任何 error 提示
- **上下文消失**：那些失败回合不会进入 Claude Code 的本地对话历史，下一次提问时模型看不到之前的上下文

从线上抓的 SSE 帧上能看到反代把 `200 OK` + `Content-Type: text/event-stream` 发出去了，但事件体要么是 OpenAI 格式（Claude Code 的 Anthropic SDK 解析器不认），要么什么都没发。

### 修复的三个 bug

- **CRIT: `/v1/messages` stream 的 pre-check early-return 写的是 JSON，不是 SSE**（[server/messages.go](internal/server/messages.go) `streamShim`）
  - `ChatCompletions` 在"model not found / not entitled / no active accounts / body too big / invalid JSON" 这些路径上直接 `writeJSON(w, 400/403/503, ...)` 返回 JSON 错误体。但我们已经给客户端发了 `200 OK` SSE header，然后把 JSON body 塞进 `streamShim.Write`，shim 把它喂给 translator 的 `feed()`，`feed()` 找 `data: ` 前缀一个都没找到，全部丢弃 → 客户端看到一个无事件的合法 SSE 流 → 空白气泡
  - 新 `streamShim` 捕获 `WriteHeader` 的 status code，非 200 时走 `errBuf`；handler 返回后调 `shim.drain()` 把 JSON error message 解出来，通过 `emitTextDelta("[Error: ...]")` 正式翻译成 Anthropic `message_start + content_block_start + content_block_delta + content_block_stop` 序列。客户端现在显示明确错误文案而非空白
- **HIGH: `finish()` 在空流上发 `message_delta` 没有前置 `message_start`**（`anthropicTranslator.finish`）
  - 任何 ChatCompletions 零 chunk 的场景（transient cascade error 全部吞完 → 空响应）都会触发：stream 里第一个事件是 `message_delta`，Anthropic 官方 SDK 的 SSE 状态机要求第一个必须是 `message_start`，直接 reject 整个 stream → 客户端把这条 turn 丢弃，**这就是"上下文消失"的直接原因**（Claude Code 不保存被 reject 的 assistant turn，下次请求的历史里不包含）
  - `finish()` 开头加一次 `t.startMessage()`（幂等，`t.started` 已是 true 就 no-op）保证前置事件一定存在
- **MED: 上游的 `: ping` 心跳被翻译器吃掉，Anthropic 客户端长思考时断连**（`anthropicTranslator.feed`）
  - chat.go 每 15 秒发一个 `: ping\n\n` SSE 注释行保活。translator 的 `feed()` 只认 `data: ` 行，ping 被静默丢弃
  - 长 thinking 阶段（20-60s 无真实 token 输出）客户端这边完全静默，超过 Anthropic 官方 SDK 的 keepalive 阈值（~30s）后客户端断连，Claude Code 按"上游无响应"处理 → 空白 + turn 丢弃
  - `feed()` 现在识别 `: ` 开头的注释行，在 `t.started == true` 时转发为 Anthropic 官方的 `event: ping\ndata: {"type":"ping"}\n\n` 保活事件

### 三条合起来的效果

Claude Code 这边看到的行为：
- 上游返回 JSON 错误 → 以前空白 / 现在显示 `[Error: <真实原因>]`
- cascade 轮询零 chunk 返回 → 以前空白 + turn 丢失 / 现在显示 `[Error: ...]` 或完整空 assistant turn，turn 保留在历史里
- 长思考阶段 → 以前 30s 断连 / 现在每 15s 一个 ping，保持连接直到真实内容到

### 版本
- `1.3.7-go` → `1.3.8-go`

## [1.3.7-go] — 2026-04-22

清完 1.3.6 遗留段列的全部四条跳过项，同时给模型目录补了手动刷新入口。

### 修复

- **R3-#6 `cache.Set` 与 `Clear` 的 lock ordering**（[cache/cache.go](internal/cache/cache.go)）—— 磁盘写在 mu 外进行，mu 外 Write 成功后取锁时若 `Clear()` 刚刚 `RemoveAll` 了目录，索引会记一个指向不存在文件的 record。加 `os.Stat(path)` 校验在索引更新前确认文件仍在磁盘；Clear 竞争时静默放弃该写入，`stores`/`bytesOnDisk` 计数不再失真
- **R3-#10 `isRateLimitedRW` 调用约定文档化**（[auth/pool.go](internal/auth/pool.go)）—— 加详细 `REQUIRES` 注释 + R2-#1 CRITICAL 历史交叉引用，阻止未来维护者把两版合并回一个函数触发 `fatal error: concurrent map read and map write`
- **R3-#13 `saveLocked` 错误可观测**（[auth/pool.go](internal/auth/pool.go) + [server/server.go](internal/server/server.go)）—— 签名加 `error` 返回；新字段 `Pool.lastSaveErr atomic.Value` 持有最近一次持久化错误，成功时自动清零；`/health`（鉴权后）新增 `persistError` 字段让运维在不翻日志的情况下立刻看到"磁盘满导致 AddByKey 没落盘"。未走破坏性的"每个 mutating API 都返回 error" 路线 — API surface 保持稳定
- **R3-#16 `modelsCatalog` 排序 O(n²) → `sort.Slice`**（[dashapi/dashapi.go](internal/dashapi/dashapi.go)）—— 模型 <80 时性能无感，但规模扩大时自然升级

### 新增

- **手动触发云端模型目录刷新**（[dashapi/dashapi.go](internal/dashapi/dashapi.go)）—— `POST /dashboard/api/models/refresh`，同步调用 `Pool.FetchModelCatalog(proxycfg.Effective)`，返回 `{before, after, added}`。日常的 2 小时定时刷新仍在跑；这个端点让 Windsurf 发新模型当天就能拉下来，不用等下个 tick
- 每次发布部署自带一次冷启动刷新 —— 1.3.7 上线日志显示：`Model catalog: 86 cloud models, 10 new entries merged`

### 版本
- `1.3.6-go` → `1.3.7-go`

## [1.3.6-go] — 2026-04-22

消化 R3 遗留条目。每一条都是"生产跑得好但审计里挂着"的改进，集中清掉。

### 性能

- **R3-#4 config 持久化改为单写者合并**（[proxycfg](internal/proxycfg/proxycfg.go) / [modelaccess](internal/modelaccess/modelaccess.go) / [runtimecfg](internal/runtimecfg/runtimecfg.go)）—— 之前每次 `save()` 起一个 goroutine，dashboard 批量 PUT（例如一次改 200 条 model-access）会瞬间产生 200 个持有 JSON 快照的 goroutine。改成单个常驻 writer + `pendingData` 合并：多次背靠背写入塌缩为"最近一次完整状态"的单次 `atomicfile.Write`
- **R3-#12 chat 热路径不再走 `Pool.All()` 深拷贝**（[auth/pool.go](internal/auth/pool.go) `HasEligible` + [server/chat.go](internal/server/chat.go)）—— `/v1/chat/completions` 每次请求先 `All()` 遍历找 eligible 账号，`cloneAccount` 会深拷贝 Capabilities/BlockedModels/ModelRateLimits/ModelRateStarted 四张 map + rpmHistory slice。30+ 账号 × 100 rps = ~1 MB/sec GC 压力仅为一个布尔 check。新 `HasEligible(modelKey, tierAllowedFunc)` 在 RLock 下直接短路，零分配

### 正确性

- **R3-#15 tier 检测正则加词边界**（[auth/ops.go](internal/auth/ops.go)）—— `(?i)pro|teams|...` 会把 `production` / `prolific` / `unpaid` 当成 Pro 账号自动升级。加 `\b...\b`，未来 Windsurf 推新 plan name 需显式扩列表
- **R3-#17 retry-after 正则要求关键字前缀**（[server/chat.go](internal/server/chat.go)）—— 原 `\b(\d+h)?(\d+m)?(\d+s)?\b` 会把任意日志里的 `5s` 读成 retry-after，把瞬态传输错误误判为限流，触发 5 分钟账号封禁。新正则 `(?i)(?:retry|wait|after|in|for)[-_ :]+((?:\d+h)?(?:\d+m)?(?:\d+s)+)\b` 必须以关键字打头才捕获

### 资源清理

- **R3-#5 `waitPortReady` 失败分支显式关 `stdinKeeper`**（[langserver/pool.go](internal/langserver/pool.go)）—— 原来依赖 `cmd.Wait` goroutine 收尾，如果 `cmd.Process.Kill()` 本身失败（权限 / PID 已回收）pipe 会一直挂着。加 `entry.stdinKeeper.Close()` 作为 belt，重复 close 是安全的

### 遗留（后续再说）

- R3-#6 cache.Set 与 Clear 的 lock ordering（stores 计数可能略偏高，dashboard 遥测层面，非阻塞）
- R3-#10 `isRateLimitedRW` 的 "caller holds write lock" 约定 - 需要项目内 race detector CI
- R3-#13 `saveLocked` 错误传播到用户可见路径 - 需要改多个 caller 的 API，成本>收益
- R3-#16 `modelsCatalog` O(n²) 排序（模型 <80 个，忽略）
- R2-#8 `logx.emit` 完成了异步写，但 subscribers map 仍在同一 mu 下（较低优先级）

## [1.3.5-go] — 2026-04-22

安全审计第三轮 + 交付质量改进。R2 之后再跑了一轮纵深审计，修完 7 条 HIGH/MED，外加集中化版本号、前后端品牌清理。

### 修复

- **R3-#1 SSE 心跳 goroutine 与 handler 生命周期未同步**（[server/chat.go](internal/server/chat.go)）—— `defer hbCancel()` 只发信号不等 goroutine 真正退出，handler return 时心跳 goroutine 可能还在 `sendRaw(": ping")` 调用 `w.Write`，触发 net/http "superfluous response.WriteHeader" 噪声甚至在 tls 层写已关闭连接。新增 `hbExited chan struct{}` + `defer func(){ hbCancel(); <-hbExited }()`，保证退出同步
- **R3-#2 `RefreshAllCredits` 阻塞放大**（[auth/ops.go](internal/auth/ops.go)）—— 旧版 `sem <- struct{}{}` 在 goroutine 外阻塞调用方：4 worker 全卡在死上游时 dashboard 按钮点 5 次 = 20 个挂起 goroutine + 5 个阻塞的 handler。改成 (a) sem push 移到 goroutine 内，(b) 5 秒冷却 + cache，并发调用共用最近一次结果，避免 refresh storm
- **R3-#3 logx 锁内同步磁盘 I/O head-of-line 阻塞**（[logx/logx.go](internal/logx/logx.go)）—— `emit` 在 `mu.Lock` 下调 `appFile.Write`，盘慢时所有 log caller（包括 chat 热路径）串行排队；30 rps × 10 log/req = 300 log/s 时尤其明显。改成 ring + subscribers 在 mu 下，落盘通过 buffered channel (`diskCh` cap 4096) 交给专用 writer goroutine，非阻塞 send，channel 满就丢
- **R3-#8 `kill_linux` cmdline 子串匹配过宽**（[langserver/kill_linux.go](internal/langserver/kill_linux.go)）—— 旧版 `strings.Contains(cmdline, "language_server")` 会误伤任何 basename 含该子串的进程。改成 exact basename match（`== "language_server_linux_x64"`）
- **R3-#9 pollCascade 共享瞬态错误计数器**（[client/client.go](internal/client/client.go)）—— 两个 gRPC unary 共用 `transientErrs`，一侧稳定失败 + 另一侧稳定成功会互相消耗预算。拆成 `stepsErrs` / `statusErrs` 独立计数；同时在 retry 分支里刷新 `lastGrowthAt` 避免 3 秒瞬态抖动被 stall 检测误判
- **R3-#11 `/self-update` 本地分支名校验**（[dashapi/dashapi.go](internal/dashapi/dashapi.go)）—— `git fetch origin -- <branch>` 虽有 `--` 终止符，但 `<branch>` 直接来自 `git rev-parse --abbrev-ref HEAD` 可能是空串 / `HEAD` / 或攻击者可写的 `.git/config` 塞进的怪字符串。新增 `isSafeBranchName` 白名单 `[A-Za-z0-9/_.-]+`（禁止空、`HEAD`、前导 `-`、`..`、长度 >200）
- **R3-#7 imagex 拒绝 `http://`**（[imagex/imagex.go](internal/imagex/imagex.go)）—— SSRF 已被 `isPrivateIP` 拦截，但纯 HTTP 下的 MITM 可注入任意字节进模型上下文相当于 prompt 投毒。仅保留 `https://` 和 `data:` 两种来源，明确拒绝 `http://` 并给清晰错误

### 版本号 & 品牌

- 新建 [internal/version/version.go](internal/version/version.go) 集中管理 `version.String`。以前 `main.go` / `server.go` / `dashapi.go` 三处各自硬编码 `"1.2.0-go"`，每次发版都有地方漏更
- 删除所有 `bydwgx1337` 引用（brand 常量 + `/health` provider 字段），发布物统一为 `"WindsurfAPI"`
- `web/package.json` 与后端对齐（1.3.x 跟随 Go 版本）
- 模型分组：GPT OSS / O-Series 并入 **OpenAI**，Claude 改名 **Anthropic**，符合品牌真实分类（[models/scoring.go](internal/models/scoring.go)）
- `DisplayName` 对版本-样式 token 整体大写：`120b → 120B` / `k2.5 → K2.5` / `m2.5 → M2.5` / `1m → 1M`

### 稳定性

- **LS 短暂断连导致对话截断**的根因修复（R3 之前一轮）—— `pollCascade` 对传输层瞬时错误（connection refused / reset / EOF / stream error / i/o timeout 等）最多吞 6 次连续失败，每次 500ms 退避继续同一 `cascade_id`；cascade 在 LS 服务端还活着时能无缝恢复。`ModelError` / ctx cancel / 上游逻辑错原样上传

### 遗留（下轮）

- R3-#4 config 包 save() 无界 goroutine 暴涨（脚本化 PUT 会放大）
- R3-#5/#6/#10/#12/#13 注释健壮性 / 小边缘 / 性能微调 / 错误传播
- R3-#15/#16/#17 tier 正则词边界、排序 O(n²)、retry-after 正则过宽

## [1.3.4-go] — 2026-04-22

安全审计第二轮。第一轮的 13 条修完 + 延后的若干条补上后，再跑了一轮纵深审计，发现 3 条 CRITICAL + 9 条 HIGH/MEDIUM，全部修完。

### 并发 / 运行时 CRITICAL

- **CRIT R2-#1: `isRateLimited` 在 RLock 下写 map**（[auth/pool.go](internal/auth/pool.go)）—— `IsAllRateLimited` 持 `p.mu.RLock()` 调用 `isRateLimited`，后者 `delete(a.ModelRateLimits, modelKey)` 过期条目。并发 `MarkRateLimited` 持写锁写同一 map → Go runtime `fatal error: concurrent map read and map write` → **进程直接崩**，不可 recover。拆成 `isRateLimited`（只读）+ `isRateLimitedRW`（写锁路径才调），`IsAllRateLimited` 改用只读版
- **CRIT R2-#2: SSE 心跳并发写 `http.ResponseWriter`**（[server/chat.go](internal/server/chat.go)）—— 心跳 goroutine 的 `fmt.Fprint(w, ": ping\n\n")` 和主循环的 `send()`/`chunkContent` 同时写 `w`，`http.ResponseWriter` 没有并发保证，SSE 帧可撕裂；Go race detector 会报。新增 `wmu sync.Mutex` + `send`/`sendRaw` helper 串行化所有写入；心跳 fire 前再次检查 `hbCtx.Err()` 防止在 handler 已退出后写回收的 response
- **CRIT R2-#3: LS `Ensure` 对非默认 key 的并发 spawn race**（[langserver/pool.go](internal/langserver/pool.go)）—— 两个请求同时 ensure `px_foo_8080`，都 miss 入口检查，各自拿递增 port 派生 LS 进程；最后一次 `p.entries[key] = entry` 覆盖前者 → **旧 LS 成无管理孤儿，StopAll 永远不会终止它**。新增 `spawning map[string]chan struct{}` 门 —— 后到者 park 在 chan 上，winner 写 `p.entries[key]` 后 close chan 释放 waiters

### HIGH / MEDIUM

- **R2-#4**: `dashapi.testProxy` 的 `connectTunnelIP` 复制了 imagex 的 DNS rebinding 漏洞 —— 只验字面 host，`evil.tld → 127.0.0.1` 照样拨号。改用 `net.Resolver.LookupIP` + 逐 IP `isPrivateIP` 过滤 + pin 到首个合法 IP（[dashapi/dashapi.go](internal/dashapi/dashapi.go)）
- **R2-#5**: `http.Server` 缺 `IdleTimeout` + `MaxHeaderBytes` → slowloris / keep-alive 洪水可拖住 FD。加 `IdleTimeout=120s` + `MaxHeaderBytes=64 KiB`；`ReadTimeout/WriteTimeout` 不设（SSE 长流会被误杀），body 大小由 `MaxBytesReader` 管控
- **R2-#6**: `proxycfg.save` / `modelaccess.save` / `runtimecfg.persist` 在 `mu.Lock` 下做同步磁盘 I/O —— chat 热路径的 `Effective()`/`Check()`/`IsEnabled()` 每次请求都要读这个 mu，盘慢 100ms 就放大成全线延迟尖刺。改成 marshal 同步（mu 下）+ 写盘 async goroutine（`saveMu` 串行化）
- **R2-#7**: `convpool` 的 `incr`/`load` 用非原子 `*p++` / `*p`，Snapshot 在 mu 外读导致 race detector 报警，32-bit / 非 x86 平台有 torn-read 风险。改用 `atomic.AddUint64` / `atomic.LoadUint64`
- **R2-#10**: `cache.Set` 还在用共享 `<path>.tmp` —— 同样的 race 1.3.3 只修了 5 个持久化路径但漏了 cache。改走 `atomicfile.Write`
- **R2-#12**: `/dashboard/api/self-update` 没有 `confirm:true` gate，dashboard 密码泄露时 CSRF 跨站 fetch 就能触发 `git pull` + 进程重启。加显式 `confirm` 检查，前端已经有二次确认弹窗
- **R2-#15**: `kill_linux` 的 orphan 扫描只看 inode 关联，PID 在高 fork 环境下可能回收。SIGKILL 前加 `/proc/<pid>/cmdline` 校验 —— 必须包含 `language_server` 才杀，否则跳过并 Warn
- **R2-#19**: `cache.Get` 读出损坏 JSON 时未清理 LRU 条目，每次 Get 都再读同一坏文件 → 永远 miss、永远不让位给新 entry。`Unmarshal` 失败分支现在也走 `removeLocked`

### 遗留（下一轮再处理）

- R2-#8 `logx.emit` 在 mu 下写磁盘的 head-of-line 阻塞 —— 需要引入带缓冲的异步 writer goroutine，改动面较大
- R2-#14 `imagex.safeDial` 当 DNS 返回公网+内网混合时一律拒绝，可能误伤 split-horizon CDN
- R2-#11 / R2-#13 / R2-#16 / R2-#17 / R2-#18 / R2-#20-#23 —— 低优先级防御加固或误报，后续迭代处理

### 其它清理

- 移除 `auth/pool.go` 未使用的 `regexp` 导入 + dead `var _ = regexp.MustCompile` 占位行
- `cloud/transport.go` `doPost` 在 Close 前 `io.Copy(io.Discard, resp.Body)` 耗尽 `LimitReader` 截断后残留的字节，让连接能重入 keep-alive pool（速率风暴下减少 socket churn）
- `RefreshAllCredits` 从完全串行改为 4-并发 —— 一个慢账号（30s 超时）不再卡住整个 15min 循环
- `Pool.RateLimitViews` 单次锁返回全部账号的 view，替代 `accountsView` 里 N 次 per-account `RateLimitView(id)` 的 thundering herd
- `cache.RequestBody` 增加 `IdentityPrompt` 字段：身份注入开关在两次请求间切换时，避免旧响应被错误 replay

## [1.3.3-go] — 2026-04-22

生产上线前的安全审计批次 —— 外部审计代理扫出的 13 条 CRITICAL / HIGH / MEDIUM 问题全部修完。

### 安全 (Security)

- **CRIT-1: `/auth/accounts` / `/auth/login` 未鉴权**（[server.go](internal/server/server.go)）—— 以前**任何匿名请求都能枚举邮箱/层级/keyPrefix 并 DELETE 任意账号**。现在与 `/v1/*` 一样挂在 `authMiddleware` 下，`/auth/status` 保持开放（只返回计数，不含标识）
- **CRIT-2: 账号池并发 save 互相覆盖**（[atomicfile](internal/atomicfile/atomicfile.go) 新增包）—— 旧版 5 个持久化路径共用硬编码 `<path>.tmp`，50min Firebase 循环 / 15min credits 循环 / dashboard POST 同时触发 save 时会交错写进同一个 `.tmp`，rename 后 `accounts.json` 变损坏 JSON，下次启动整池失联。改用 `crypto/rand` 生成每次调用唯一 tmp 名（`accounts.json.<hex>.tmp`），5 处（`auth/pool`、`proxycfg`、`modelaccess`、`runtimecfg`、`stats`）统一走 `atomicfile.Write(path, data)`，同时把权限统一到 0o600
- **CRIT-1: 账号池并发 map 读写崩溃**（[auth/pool.go cloneAccount](internal/auth/pool.go)）—— `All()`/`Get()` 以前用 `cp := *a` 浅拷贝，`Capabilities/BlockedModels/ModelRateLimits/ModelRateStarted` map/slice 字段仍指向池里活对象。dashboard 在持锁外遍历这些 map，同时 probe/markRateLimited 持锁写同一 map，触发 Go runtime 的 `fatal error: concurrent map read and map write` —— **进程直接崩溃不可恢复**。改成显式 `cloneAccount()` 深拷贝全部 map/slice
- **HIGH-4: 图片 URL SSRF DNS rebinding**（[imagex.go fetchURL](internal/imagex/imagex.go)）—— 旧版只校验 URL 里字面 host，`evil.example.com` 解析到 `169.254.169.254` 照样拨号。新 `safeDial` 走自定义 `net.Resolver.LookupIP` → 对每个返回 IP 跑 `isPrivateIP` → 拨号钉到第一个通过校验的 IP（不让 happy-eyeballs 替换成没校验过的 IP）
- **HIGH-14: LS adoption 用错 CSRF**（[langserver/pool.go](internal/langserver/pool.go) + [kill_linux.go](internal/langserver/kill_linux.go) 新增）—— 旧版在启动时发现 42100 端口被占用就直接 "adopt"，但每次进程启动都重算 `DefaultCSRF`，**被领养的 LS 使用旧进程的 CSRF**，随后所有 gRPC 调用都 `permission_denied`，整池失服。改成：扫 `/proc/net/tcp{,6}` + `/proc/*/fd` 找到占端口的 PID 后 `SIGKILL`，等 2 秒端口释放后再用**当前进程的 CSRF** 正常 spawn。非 Linux 平台走 build tag no-op
- **HIGH-19: API Key / 密码定时攻击**（[server.go authMiddleware](internal/server/server.go) + [dashapi.go secretEqual](internal/dashapi/dashapi.go)）—— `==` 字符串比较可通过网络 RTT 逐字节爆破。改成 `crypto/subtle.ConstantTimeCompare` + 长度预检，空密码/不配置场景显式 false
- **HIGH-11: `writeJSON` 覆盖 CORS 白名单**（[dashapi.go](internal/dashapi/dashapi.go)）—— `writeJSON` 硬写 `Access-Control-Allow-Origin: *`，**把 `CORS_ALLOWED_ORIGINS` 配置整个作废**。改成删除硬编码，让外层 `cors()` 中间件的白名单生效
- **HIGH-13: `SetStatus` 未校验输入**（[auth/pool.go validStatus](internal/auth/pool.go)）—— 以前 dashboard PATCH 可以把账号写成任意字符串（"hacked"）导致 `StatusActive` 判断永远跳过。现在只接受 `active/error/disabled/expired/invalid` 5 值白名单
- **HIGH-26: 请求体 32MB → 8MB**（[chat.go](internal/server/chat.go) + [messages.go](internal/server/messages.go)）—— 1GB VM 上 30 个并发 32MB body 可触发 OOM。图片走 imagex 5MB 独立通道，8MB body 足够所有真实请求
- **HIGH-34: LS 启动日志包含代理密码**（[langserver/pool.go proxyLabel](internal/langserver/pool.go)）—— 旧版写 `proxy=http://user:pass@host:port` 到 `logs/app-*.jsonl`。新 `proxyLabel()` 只保留 `host:port`，credentials 不落盘
- **HIGH-15: git argv 防 `-` 前缀 ref 注入**（[dashapi.go selfUpdate](internal/dashapi/dashapi.go)）—— `git fetch origin <branch>` / `git reset --hard origin/<branch>` 全改成 `... -- <branch>` 加选项终止符，缓解 CVE-2018-17456 类。`git pull` 同理
- **MED-20: `/v1/messages` 异常响应 index-out-of-range panic**（[messages.go](internal/server/messages.go) `openAIToAnthropicResponse`）—— 空 `Choices` 切片直接 `[0]` 崩服务。加 `len == 0` 兜底返回空 assistant turn

### 并发 / 稳定性

- **HIGH-6: `Periodic` WaitGroup race**（[auth/ops.go](internal/auth/ops.go)）—— 旧版 credits / firebase 循环在已启动的 goroutine 内再 `wg.Add(1)`，违反 `sync.WaitGroup` 文档（"Add calls must happen before Wait"）。Add 被提到 spawn 之前，`wg.Add(2)` 一次性涵盖"立即跑"+"定时跑"两个 goroutine

### 文档

- [ENV_VARS.md](docs/ENV_VARS.md) 新增**响应缓存**章节，已在 1.3.2 提到；本版本不变

## [1.3.2-go] — 2026-04-22

运行时资源调整与控制台可读性改进。

### 变更

- **响应缓存全面改为磁盘后端**（[`internal/cache/cache.go`](internal/cache/cache.go) 全量重写）
  - 容量 `500 → 6000` 条，TTL `5 分钟 → 2 小时`
  - Entry 体（Text + Thinking）写到 `/tmp/windsurfapi-cache/<sha256>.json`，不再驻留 Go heap —— 默认路径是 Debian systemd 的 tmpfs，内存充裕时在 RAM、压力上来时内核自动把冷文件 spill 到 swap，满足"缓存全部写入 SWAP"的部署要求
  - `CACHE_PATH` 环境变量可改走持久化磁盘（如 `/opt/windsurfapi/cache`），跨重启保留
  - 原子写（`.tmp` + rename）防撕裂读；启动时扫目录重建索引，过期就地清理；`Clear()` 现在会删 `dir`
  - 内存占用：新版只保留 key + expiresAt + LRU 节点（约 200 B/条），6000 条索引仅 ~1.2 MB。以 3 KB 平均响应计，磁盘侧 6000 条约 18 MB
  - `Snapshot()` 新增 `backing`（当前路径）与 `bytesOnDisk`（磁盘占用字节数）
- **统计分析页 p50 / p95 汉化 + 单位**（[`web/src/views/Stats.vue`](web/src/views/Stats.vue)）
  - 表头：`p50 → 中位延迟` / `p95 → 尾部延迟` / `均值 → 平均耗时`
  - 单元格加 ms/s 单位渲染（裸数字 `1234` 改成 `1.23 s`；`456 ms`）
  - `customHeaderCell` 加了 `title` 悬停提示说明分位数语义
  - 时间窗口按钮：`近 6h / 24h / 72h` → `近 6 小时 / 24 小时 / 72 小时`；监控窗口卡的 `${buckets.length}h` 也改成 `N 小时`
- **上游状态码原因汉化**（[`web/src/views/Overview.vue`](web/src/views/Overview.vue)）
  - chip 格式从"code code count"改成"code · 原因 · N 次"
  - 新增 `statusReason` 映射表覆盖 20+ 常见码（200 正常 / 429 限流 / 504 连接超时 / 520 CDN 未知错误 / 522 连接超时 / 524 上游响应超时 等）
  - 未命中映射时按段落兜底（2xx 成功 / 3xx 重定向 / 4xx 客户端错误 / 5xx 服务端错误）

## [1.3.1-go] — 2026-04-22

上游 JS 仓 (`dwgx/WindsurfAPI`) 近期提交的 backport。对照 78 条 commits 做了能力缺口审计，吸收对 Go 反代有意义的增量。

### 新增

- **图片 / 视觉输入（backport `fad32d3` + `1f98fff` + `ee3c413`）**
  - 新模块 `internal/imagex`：把 `data:image/...;base64,...` data URL、远程 `https?://` URL、裸 base64 三种客户端输入形态归一成 `{Base64, Mime}`
  - 远程抓取走 SSRF 白名单：拒绝 `127/10/172.16-31/192.168/169.254/100.64/fe80/fc00` 及 `localhost`/`.local`/`.internal`/`metadata.*`；每次 redirect 重新校验目标避免 302 绕过；3 跳上限；5 MB 解码大小上限；严格 https/http 协议
  - `SendUserCascadeMessageRequest.images` proto field 6（repeated `ImageData{base64_data=1 string, mime_type=2 string}`）。`base64_data` 用 string 而非 bytes（raw bytes 触发 LS "string field contains invalid UTF-8"，已在上游 740ad6d 验证）
  - **有图片时 planner_mode 从 NO_TOOL(3) 切回 DEFAULT(1)**——NO_TOOL 下视觉管线不启用，图片会被 LS 默默丢弃（上游 1f98fff 验证）；无图片时维持 NO_TOOL 保留本项目现有反射性工具回路抑制
  - OpenAI 端：`/v1/chat/completions` 识别 `content: [{type:"image_url", image_url:{url:"..."}}]` 形态
  - Anthropic 端：`/v1/messages` 识别 `{type:"image", source:{type:"base64", media_type, data}}`，内部翻译时转成 OpenAI 形态，`extractImages()` 一个口子走通
- **官方 dated 模型别名（backport `efcb713` + `a6376f8`）**
  - `claude-opus-4-5-20251101` / `claude-sonnet-4-5-20251101` / `claude-3-7-sonnet-20250219` / `claude-3-5-sonnet-20241022` 等 Anthropic SDK 固定写法自动解析到短名
  - `gpt-4o-2024-08-06` / `gpt-4.1-2025-04-14` / `gpt-5-2025-08-07` 等 OpenAI SDK dated snapshot 全量覆盖
  - `-latest` / `-0` / Claude 4.7 `claude-opus-4-7` → `claude-opus-4-7-medium` 默认变体
  - 未命中目录的别名自动跳过（不污染 Resolve），防止云端模型还没 merge 进来时 dangling key
- **gRPC 多帧响应解析（backport `13c72a0` / `f9678ae`）**
  - 新增 `grpcx.ExtractFrames`：遍历并拼接所有 length-prefixed 帧的 payload。原 `StripFrame` 只处理首帧，遇到 LS 偶尔把大 trajectory 响应切到 2+ 帧时会静默截断。单帧响应行为不变

### 审计对齐

对照上游 `8f1b50e / fe4ddb1 / 9fad3ac / d02efc3 / 05e8519 / 2c993b9 / 3a45f56 / 7f339b5 / b7937b0 / ef21ff7` 等关键协议 / 账号逻辑修复，确认 Go 端已覆盖：
- ✅ CascadeConversationalPlannerConfig 四字段同时填（plan_model_deprecated 1 / requested_model_deprecated 15 / plan_model_uid 34 / requested_model_uid 35）
- ✅ Legacy RawGetChatMessage 的 assistant 编码走 ChatMessage.action(6) → ChatMessageAction.generic(1) → ChatMessageActionGeneric.text(1)
- ✅ role=tool 降级 "[tool result for X]: ..."、assistant.tool_calls 降级 "[called tool X with Y]"
- ✅ planner_mode=NO_TOOL(3) + tool_calling_section(10) + additional_instructions_section(12) 双层 section override
- ✅ per-model 限流（`ModelRateLimits` + `ModelRateStarted`，持久化）
- ✅ LS per-proxy pool，`getLsFor` 不 fallback default
- ✅ 动态账号重试上限 `clamp(active, 3, streamMaxTries=10)`
- ✅ gRPC 连接错误不累计账号错误计数
- ✅ Firebase 50 分钟周期 refresh
- ✅ SIGTERM 前原子落盘（`signal.NotifyContext` + 优雅关停，`auth` 包的 saveLocked 保证持久化）
- ✅ `/v1/messages` 接受 `x-api-key` 头

## [1.3.0-go] — 2026-04-22

控制台能力扩展与若干稳定性修复。

### 新增

- **仪表盘系统指标卡栏**：CPU 使用率 / 内存 / SWAP / 下行带宽 / 上行带宽 / 系统负载 —— 均通过新模块 `internal/sysinfo` 直接读取 `/proc/stat`、`/proc/meminfo`、`/proc/net/dev`、`/proc/loadavg`，无第三方依赖；超 70% 染黄、超 90% 染红；Windows/macOS 下返回零值而非崩溃
- **仪表盘 Token 与费用卡栏**：总 Token 消耗（输入 / 输出 / 总量）与等价总费用（USD），按各家官方公开价格表折算。价格表见 `internal/models/pricing.go`，涵盖 50+ 主流模型，未登记的按默认 `$1/M input · $5/M output` 保底
- **仪表盘"模型清单"框**：按厂商分组（Claude / GPT / Gemini / DeepSeek / Grok / Qwen / Kimi / GLM / MiniMax / Windsurf SWE / Arena），每款模型显示展示名 + 模型 ID + 能力总分（0-100）。能力评分表在 `internal/models/scoring.go`，手工校准 80+ 条目；未命中表的新模型通过 `inferScore()` 基于家族基分 + 版本 + 后缀（`-high/-low/-xhigh/-max/-thinking/-mini/-nano` 等）推断
- **仪表盘上游状态码分布**：LS 实例卡底部按色标展示 2xx / 3xx / 4xx / 5xx / 传输错误的直方图
- **仪表盘版本 + 可用模型数卡栏**：版本号（来自 `/health`）+ `<允许数> / <总数>` 以及当前访问模式（全部放行 / 允许清单 / 封锁清单）
- **鼠标悬停滚动长名称**：新组件 `MarqueeText.vue`，长度超容器时悬停左滑显示尾部，右边界带遮罩淡出；离开后回弹
- **模型名称统一格式化**：工具 `web/src/utils/modelName.ts` 与 Go 侧 `models.DisplayName()` 双向一致，将 `claude-opus-4-7-high` 等云端模型 UID 自动还原为 `Claude Opus 4.7 High`；统计分析、异常监测、仪表盘三处都走同一格式
- **模型目录云端刷新**：`FetchModelCatalog` 从启动一次改为每 **2 小时**定时拉取，新发布的云端模型自动进入目录，无需重启
- **异常监测层级大小写**：`pro → Pro`、`free → Free`、`expired → Expired`、`unknown → Unknown`，原始枚举值只保留在 API 层

### 修复

- **LS 连接 `dial tcp [::1]:<port>: connection refused` 间歇性报错**：gRPC 客户端基址从 `http://localhost:<port>` 改为 `http://127.0.0.1:<port>`，绕过 IPv6 `::1` 优先解析与 LS 仅绑定 IPv4 之间的不匹配（`internal/grpcx/grpcx.go`）
- **LS 领养的僵尸 Entry**：端口已被占用时的"领养"分支没有 `Process` 引用、没有 `cmd.Wait()` 监控；被领养的进程死了之后 Entry 永久残留，所有请求打死端口。新增领养专用看门狗 goroutine，每 5s 探测一次端口，连续两次不通就从池里删除 Entry 触发下次请求重新 `Ensure`（`internal/langserver/pool.go`）
- **LS 重启成功提示显示红 X 无内容**：`toast({ message: '已触发重启' }, '')` 把非 Error 对象传给只调 `message.error()` 的 toast helper，结果渲染空错误；改为直接 `message.success('已触发重启')`

### 移除

- 仪表盘右上角的"检查更新 / 一键更新并重启"按钮与对应的 `<Alert>` 提示条：实际部署以 systemd + SFTP 为主，旧 `git pull` 自更新路径对当前部署方式无意义

### 改进

- 服务端配置卡栏的"默认模型"字段走 `displayModelName()` 转成易读名称；"最大 tokens" 标签改为 "最大 Token"
- 厂商分组将 OpenAI O-Series（`o3` / `o3-pro` / `o4-mini`）合并进 GPT；云端 `MODEL_*` 前缀带 UID 的条目（如 `model-claude-4-opus-byok`）正确归入 Claude 组
- 领养 Entry 由 `Ready: true` 标记，新增 `convpool.InvalidateFor` 清理，避免在 LS 漂移时复用失效的 cascade_id

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
