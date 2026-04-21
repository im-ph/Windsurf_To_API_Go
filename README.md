# WindsurfAPI · Go 重写版

> **使用边界**：本项目仅供个人学习、研究与自用。**严禁商业转售、对外中转、付费部署、"共享账号"式代理服务**或任何以本软件为基础的营利性运营。违反者与维护者无关，出现的后果自负。底部的 MIT 条款只是开源协议形态，并不解除上述使用边界。

将 Windsurf 的官方 Language Server 封装为 OpenAI / Anthropic 兼容的单文件反向代理，自带管理控制台。零运行时依赖，除同目录下的 `language_server_linux_x64` 外无需额外文件。

- **语言 / 运行时**：Go ≥ 1.26.2，静态编译，Linux amd64 产物约 13 MB（含内嵌 Vue SPA）
- **内嵌前端**：Vue 3 + TypeScript + Ant Design Vue 4，`//go:embed` 打入二进制
- **兼容协议**：OpenAI `/v1/chat/completions` + Anthropic `/v1/messages`（原生 SSE 透传，支持图片输入）
- **账号池**：分层 RPM 加权调度、按模型级别的限速/限流隔离、Firebase 令牌自动刷新
- **控制台**：9 个页面（仪表盘 / 统计分析 / 登录取号 / 账号管理 / 异常监测 / 模型控制 / 代理配置 / 运行日志 / 实验性功能），支持实时系统指标、模型能力清单、Token 消耗与等价费用统计

## 快速开始

```bash
git clone https://cnb.cool/Neko_Kernel/Windsurf_To_API_Go.git
cd Windsurf_To_API_Go

# 前端构建产物（dist/）已随仓库提交，如要改前端：
# pnpm --dir web install && pnpm --dir web build

# 编译（Linux 生产）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags='-s -w' -trimpath \
  -o windsurfapi ./cmd/windsurfapi

# 本机跑起来（端口默认 3003，控制台 /dashboard）
cp .env.example .env
./windsurfapi
```

浏览器打开 `http://<host>:3003/dashboard` 即可。

## 文档索引

| 文档 | 内容 |
|---|---|
| [docs/INSTALL.md](docs/INSTALL.md) | 从源码 / 预编译二进制两种安装路径、依赖、Language Server 放置方式 |
| [docs/USAGE.md](docs/USAGE.md) | 控制台各面板用法、API 接入示例、常见排障 |
| [docs/DEPLOY.md](docs/DEPLOY.md) | systemd 生产部署、专用用户、文件权限、自更新 |
| [docs/ENV_VARS.md](docs/ENV_VARS.md) | 所有环境变量、默认值、优先级、安全注意 |
| [docs/API.md](docs/API.md) | HTTP 接口参考（聊天、Anthropic 消息、认证、控制台 API） |
| [CHANGELOG.md](CHANGELOG.md) | 版本记录 |

## 目录结构

```
Windsurf_To_API_Go/
├── cmd/windsurfapi/main.go       程序入口
├── internal/
│   ├── auth/                     账号池（分层 RPM、限流/限速、能力探测）
│   ├── cache/                    精确响应缓存（按请求体哈希）
│   ├── client/                   WindsurfClient（Cascade 流程 + 停滞保护）
│   ├── cloud/                    Codeium REST（GetUserStatus 等）
│   ├── config/                   .env + 类型化配置
│   ├── convpool/                 Cascade 对话 ID 复用池
│   ├── dashapi/                  /dashboard/api/* 全部路由
│   ├── firebase/                 Firebase 登录 / 令牌刷新 / 再注册
│   ├── grpcx/                    HTTP/2 gRPC 单次 + 流式（h2c，多帧响应自动拼接）
│   ├── imagex/                   图片输入处理（data URL / 远程 URL / 裸 base64，SSRF 白名单）
│   ├── langserver/               Language Server 进程池（一代理一实例）
│   ├── logx/                     环形缓冲 + SSE 广播 + JSONL 日志滚动
│   ├── modelaccess/              全局模型白/黑名单
│   ├── models/                   模型目录（120+ 条）+ 层级访问表 + 能力评分 + 定价表
│   ├── pbenc/                    零依赖 protobuf 编解码
│   ├── proxycfg/                 全局 + 账号级 HTTP/HTTPS/SOCKS5 代理
│   ├── runtimecfg/               运行时配置（实验性开关 + 身份提示模板）
│   ├── sanitize/                 流式路径脱敏（/tmp/windsurf-workspace）
│   ├── server/                   HTTP 路由 + chat / messages / 探测
│   ├── stats/                    每模型 / 每账号 / 72h 桶 + p50/p95 + Token 消耗 + USD 等价费用
│   ├── sysinfo/                  实时系统指标采样（/proc 读取，CPU/内存/SWAP/网络/负载）
│   ├── toolemu/                  OpenAI tools[] ↔ Cascade 文本协议模拟
│   ├── web/                      内嵌前端（Vite 构建产物 + //go:embed）
│   └── windsurf/                 Cascade + Legacy 协议 builder/parser
├── web/                          Vue 3 前端源码
│   ├── src/
│   │   ├── api/                  统一请求封装 + 按资源拆分
│   │   ├── components/           可复用组件
│   │   ├── composables/          组合函数（Firebase OAuth 等）
│   │   ├── layouts/              BasicLayout（sider + drawer 自适应）
│   │   ├── router/               路由 + 守卫
│   │   ├── stores/               Pinia 状态
│   │   ├── styles/               全局主题
│   │   └── views/                9 个业务页
│   ├── package.json
│   └── vite.config.ts
├── bin/language_server_linux_x64 Windsurf 官方 Language Server 二进制
├── go.mod / go.sum
├── .env.example                  环境变量模板
└── docs/                         完整文档
```

## 控制台亮点

- **仪表盘（Overview）**：活跃账号 / 总请求 / 运行时间 / LS 状态 / 响应缓存 + **实时系统指标**（CPU、内存、SWAP、下行/上行带宽、系统负载 1/5/15）+ **Token 消耗**（输入/输出/总量）+ **等价总费用**（按各家官方公开定价折算）+ 可用模型数 + 版本号；底部新开"模型清单"框，按厂商分组（Claude / GPT / Gemini / Kimi / GLM / …），每个模型显示能力总分，长名称鼠标悬停时自动左滑展示完整 ID；底部还附上游 HTTP 状态码直方图（2xx/4xx/5xx/传输错误按色分）
- **统计分析**：按小时粒度（6h / 24h / 72h 可切）的请求量柱图、模型维度与账号维度表（p50 / p95 / 错误率），模型名显示为人类可读格式（`Claude Opus 4.6 Thinking`）
- **登录取号**：Firebase 邮箱密码 + Google / GitHub OAuth（OAuth 仅在 `windsurf.com` / `localhost` 来源下可用）；支持一次批量取号，成功 / 失败都入库
- **账号管理**：36+ 账号的分页视图，手动层级覆盖（"Free 试用用户被识别为 Pro"的兜底）、主动能力探测、日 / 周额度剩余百分比按色显示、限流状态可视
- **异常监测**：全账号按严重度排序，展示限流与**限速**（per-model 窗口），附"北京时间 HH:mm 开始 — 北京时间 HH:mm 解除"原因说明，到期自动放回号池
- **模型控制**：全局允许 / 封锁清单、访问模式三档切换
- **代理配置**：全局 + 账号级 HTTP/HTTPS/SOCKS5，支持在主机字段直接贴 `host:port`，代理测试返回出口 IP + 延迟
- **运行日志**：SSE 实时流 + 级别筛选 + 关键字搜索，填满视口高度
- **实验性功能**：`cascadeConversationReuse` / `modelIdentityPrompt` / `preflightRateLimit` 等开关可以界面切换

## 性能与 JS 版对比

| | Node.js 原版 | Go 重写版 |
|---|---|---|
| 分发产物 | Node 运行时 + 源码目录 | 单个静态二进制（约 9 MB） |
| 空载内存 | 70 – 90 MB | 10 – 15 MB |
| 并发模型 | 单事件循环 | 协程 + 每请求 context |
| protobuf 分配 | `Buffer.concat` 重复拷贝 | `append([]byte, …)` 单次外层分配 |
| gRPC 连接 | 每次轮询新建 | `http2.Transport` 连接池 |
| SSE 吞吐 | Node Writable 中间缓冲 | 直接 `http.Flusher` |
| 路径脱敏 | 每 chunk 正则 replaceAll | 流式带尾部保留的 `Stream` |

## 与上游 Windsurf 的兼容要点

- `planner_mode = NO_TOOL (3)`，并对 `CascadeConversationalPlannerConfig` 字段 10/12/13 加三条 `SectionOverrideConfig` 覆盖 —— 关闭 Cascade 内置的 IDE 代理回路，避免 `stall_warm` 伪阳性、`/tmp/windsurf-workspace` 路径泄露、文件冲突
- 同时发送 `requested_model_uid`（字段 35）和弃用的 enum（字段 15 / ModelOrAlias.字段 1）—— 用户状态为空时两者缺一会被上游拒
- 流式中优先 `responseText`，只在空闲时用 `modifiedText` 做前缀扩展补齐
- 冷启动停滞阈值 `30s + ⌊chars/1500⌋·5s`，封顶 180s
- 热启动停滞：25s 无进度 → 已输出 <300 字符则重试，否则接受当前结果
- 账号信号分流：限速 / `permission_denied` / `failed_precondition` / `internal error` 各走隔离路径，只有真实认证失败才扣错误预算
- 限速窗口**持久化**到 `accounts.json`，重启不丢
- Firebase API Key `AIzaSyDsOl-1XpT5err0Tcnx8FFod1H8gVGIycY` 为官方固定值，不要轮换
- Linux 启动时擦除 `/tmp/windsurf-workspace/*`
- Dashboard 密码未设置时回退到 `API_KEY`

## 已知限制

- **`modelIdentityPrompt` 对 grok 模型无效**：grok 的 RLHF 偏见会让模型回"我是 Cascade"。该开关默认开启，因为它让 Claude 的身份更稳固，但 grok 会偶发无视指令。需要纯净身份时请把 `DEFAULT_MODEL` 换成 `claude-4.5-haiku`（1x 额度，身份稳定），或在控制台实验性功能面板把 `modelIdentityPrompt` 关闭
- **Firebase OAuth 受来源域限制**：Google / GitHub 一键登录依赖 Firebase Auth，后者只允许 `windsurf.com` 和 `localhost` 作为来源。自建 IP 部署时走 SSH 隧道 `ssh -L 3003:localhost:3003` 后从 `http://localhost:3003/dashboard` 访问即可
- **自更新**：只执行 `git pull` + `os.Exit(0)`，依赖 systemd / PM2 等进程管理器自动拉起

## 协议

[MIT License](LICENSE)。
