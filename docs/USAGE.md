# 使用指南

## 控制台 9 个页面

浏览器打开 `/dashboard`，以 `DASHBOARD_PASSWORD`（未设置则为 `API_KEY`）登录。

### 1. 仪表盘

- 5 个指标卡：活跃账号 / 总请求数 / 运行时间 / Language Server 状态 / 缓存命中率
- Language Server 实例面板：每个独立代理对应一个 LS 实例
- 服务端配置：端口、默认模型、日志级别、LS 二进制路径等，一键清空响应缓存
- 顶部按钮：检查更新（git fetch 对比）+ 一键更新重启（需要 systemd/PM2 管着）

### 2. 统计分析

- 请求量 6h / 24h / 72h 时间序列柱图
- 按模型聚合：请求量 / 成功率 / 均值 / p50 / p95 延迟
- 按账号聚合：请求量 / 成功率
- 重置按钮：清零所有统计

### 3. 登录取号

三条获取账号的路径：
- **OAuth 登录**（Google / GitHub）：最简洁。只在来源域是 `localhost` 或 `windsurf.com` 时可用 —— 自建 IP 部署请走 SSH 隧道 `ssh -L 3003:localhost:3003`
- **邮箱密码**：走 Firebase REST。数据中心出口 IP 被风控时可能报"邮箱或密码错误"，此时在"登录代理"区填一个住宅代理
- **粘贴 Auth Token**：从 `windsurf.com/show-auth-token` 复制 token 到"账号管理 → 添加账号"，无需 Firebase 交互

登录历史保存在浏览器 localStorage，最近 50 条，状态/错误原因/使用的代理一目了然。

### 4. 账号管理

- 列表展示：ID / 邮箱 / 层级 / RPM / 余额（日+周 % 剩余）/ 可用模型数 / 状态 / 最近探测 / Key 前缀
- 每行操作（6 个按钮）：
  - 🔍 **探测**：对该账号发一次探测请求，更新层级和能力
  - 💰 **余额**：调 `GetUserStatus` 刷新日/周额度
  - ⚡ **限流**：查询 `CheckMessageRateLimit`，Pro 显示"无消息数限流"
  - 🔑 **刷新**：用 refreshToken 换新 API Key（适用于 OAuth/邮密登录的账号）
  - ⏸ / ▶ **停用 / 启用**：保留账号配置但从轮询中移出
  - 🗑 **删除**：永久移除
- 层级下拉可手动覆盖（`TierManual=true` 后自动探测不会再改）
- 顶部工具栏：**刷新余额**（全量）/ **全部探测** / **刷新**

### 5. 异常监测

- 指标卡：错误账号 / 过期账号 / 限流中 / 限速中 / 已停用
- 账号健康状况表（展示全部账号，问题账号自动置顶）：
  - 状态：`正常 / 错误 / 已过期 / 无效 / 已停用`，被限速时额外叠加黄色"限速"
  - 限流列：⚡ 标签，账号级粗粒度标记
  - 限速列：**按模型级别**列出被官方限速的 (账号, 模型) 对，附倒计时（秒级刷新）
  - 操作列：限速中显示"`xxx` 模型被官方限速，北京时间 A 开始，B 解除"，到期自动恢复无需手动

### 6. 模型控制

- 访问策略三选一：**全部允许** / **白名单** / **黑名单**
- 白/黑名单模式下展开模型清单 —— 按供应商分组（Anthropic / OpenAI / Google / DeepSeek / xAI / Alibaba / Moonshot / Windsurf），可搜索、可按供应商筛选
- 点击 chip 即时加入或移除

### 7. 代理配置

- **全局代理**：所有未配置独立代理的账号走这里，支持 HTTP / HTTPS / SOCKS5，可带账密
- **新增账号级代理**：从下拉选择账号（已绑定的自动过滤），保存后会为该账号启动独立的 LS 实例
- **账号独立代理表**：展示已绑定的账号及其代理，可清除
- 主机字段可直接贴 `host:port`，端口字段留空会自动拆分
- 测试按钮：实际走一次 HTTP CONNECT 握手，返回出口 IP + 延迟

### 8. 运行日志

- SSE 实时流式接收服务端日志
- 级别过滤：全部日志 / Debug / Info / Warn / Error
- 内容搜索 + 自动滚动开关
- 日志容器自适应填满视口
- 缓冲上限 1000 条

### 9. 实验性功能

- **Cascade 对话复用**：多轮对话时复用 `cascade_id`，只发最新 user 消息给 Windsurf，让服务端维持上下文缓存。命中时可显著降低 TTFB + 上传体积；未命中自动回退到新会话。需要客户端保留完整历史并按顺序追加（new-api、OpenWebUI 满足）。默认关闭
- **模型身份注入**：每个请求最前面注入一条系统提示覆盖 Cascade 自带的 Windsurf 身份。每个厂商模板可编辑，`{model}` 占位符自动替换。对 Claude 系生效稳定，对 grok 系 RLHF 偏见压不住（见 README 已知限制）

## API 接入示例

### OpenAI 客户端

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://<host>:3003/v1",
    api_key="<API_KEY>",
)

resp = client.chat.completions.create(
    model="claude-4.5-sonnet-thinking",
    messages=[{"role": "user", "content": "写一首关于 Go 的俳句"}],
    stream=True,
)
for chunk in resp:
    print(chunk.choices[0].delta.content or "", end="", flush=True)
```

### Anthropic 客户端

```python
from anthropic import Anthropic

client = Anthropic(
    base_url="http://<host>:3003",
    api_key="<API_KEY>",
)

with client.messages.stream(
    model="claude-4.5-sonnet-thinking",
    max_tokens=1024,
    messages=[{"role": "user", "content": "你好"}],
) as stream:
    for text in stream.text_stream:
        print(text, end="", flush=True)
```

### curl

```bash
curl -X POST http://<host>:3003/v1/chat/completions \
  -H 'Authorization: Bearer <API_KEY>' \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"claude-opus-4.6",
    "stream":true,
    "messages":[{"role":"user","content":"你好"}]
  }'
```

## 常见排障

### 启动时报 "Language server binary not found"

未找到 `language_server_linux_x64`。查找顺序见 [INSTALL.md](INSTALL.md)，最简单的做法是把 `bin/` 目录放在 `windsurfapi` 可执行文件旁边。

### LS 启动后 1 秒就退出

检查 `/opt/windsurf/data` 是否可写。LS 需要 `--codeium_dir` 和 `--database_dir`，默认在 `/opt/windsurf/data/<proxy_key>`。

### Firebase 登录一直失败

服务端出口 IP（尤其 AWS / Azure 数据中心）被 Firebase 风控。解决：
1. 在"登录取号"页配一个**住宅代理**再登录
2. 或用 Google/GitHub OAuth（但需要 localhost 域名）
3. 或直接粘贴 Auth Token 添加账号（从 `windsurf.com/show-auth-token` 获取）

### 日志里看到 `panel state not found` / `path is already tracked`

这是 Cascade 面板状态被 GC 后的自动重建流程，服务内部有完整的恢复路径，已在日志级别上降为 DEBUG，不会打到默认面板。

### 限速/限流在重启后没保持

从 v1.2.0 起**已持久化**。`accounts.json` 会同时保存开始时刻 + 解除时刻，重启只清理已经过期的窗口，未到期的保留。

### 控制台打开后必须手动刷新才能看到数据

已修复。所有 9 个页面都在 `onMounted` 自动加载，Overview / Stats / Accounts / Logs 还有轮询或 SSE 持续更新。

### 上游错误里带 `27m31s` 这类重试时长没被用上

服务端会从错误文本解析 Go 风格时长、散文（`retry after 30 seconds`）、键值（`retry_after: 30`）三种格式。解析后按 `ceil(d, 1 min) + 1 min 缓冲` 计算实际窗口，例如 `5m → 6m`、`27m31s → 29m`。
