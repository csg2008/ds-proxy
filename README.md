# ds-proxy

> DeepSeek V4 Thinking Mode 代理修复工具 —— 解决 Claude Code 调用 DeepSeek V4 系列模型时的 `400 content[].thinking` 错误。

## 问题背景

DeepSeek V4 系列模型（包括 V4 Flash）在 **Thinking 模式**下会返回思维链内容（`reasoning_content` / `thinking` 块）。DeepSeek API 要求多轮对话中必须将之前 assistant 消息中的 `thinking` 块原样回传，否则第二轮请求（尤其是涉及 Tool Calls 后）会被拒绝，返回：

```
API Error: 400 The content[].thinking in the thinking mode must be passed back to the API.
```

Claude Code 在序列化历史消息时会丢弃这个非标准字段，导致该错误频繁出现。本项目通过在 Claude Code 与 DeepSeek API 之间插入一个轻量级代理，**自动补全**缺失的 `thinking` 字段，从而彻底解决这个问题。

## 功能特性

- ✅ **零外部依赖**：纯 Go 标准库实现，单二进制文件即可运行
- ✅ **自动修复**：智能检测并补全缺失的 `thinking` / `reasoning_content` 字段
- ✅ **流式透传**：原生支持 SSE 流式响应，零拷贝透传
- ✅ **轻量高效**：内存占用极低（~10 MB），高并发无压力
- ✅ **配置灵活**：支持命令行参数与环境变量自定义监听地址和目标 API 地址
- ✅ **调试友好**：内置 `-debug` 开关，可打印请求与响应明细
- ✅ **CORS 支持**：内置跨域头，兼容浏览器调试场景

## 安装

### 前置要求

- Go 1.21 或更高版本

### 编译

```bash
git clone https://github.com/csg2008/ds-proxy.git
cd ds-proxy
go build -o ds-proxy
```

编译完成后，当前目录会生成名为 `ds-proxy`（Windows 为 `ds-proxy.exe`）的可执行文件。

## 使用

### 直接运行

```bash
./ds-proxy
```

默认配置：
- 监听地址：`127.0.0.1:15722`
- 目标 API：`https://api.deepseek.com`

### 命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `-port` | 本地代理监听端口 | `15722` |
| `-host` | 本地代理监听主机 | `127.0.0.1` |
| `-upstream` | 上游模型 API 地址 | `https://api.deepseek.com` |
| `-debug` | 调试开关，开启后打印请求与响应明细 | `false` |

示例：

```bash
./ds-proxy -host 0.0.0.0 -port 8080 -upstream https://api.deepseek.com -debug
```

### 环境变量

命令行参数的默认值会读取对应的环境变量，便于通过环境配置：

| 环境变量 | 说明 | 默认值 |
|---------|------|--------|
| `PORT` | 本地代理监听端口 | `15722` |
| `HOST` | 本地代理监听主机 | `127.0.0.1` |
| `DEEPSEEK_HOST` | 上游模型 API 地址 | `https://api.deepseek.com` |

示例：

```bash
PORT=8080 DEEPSEEK_HOST=https://api.deepseek.com ./ds-proxy
```

### 后台运行

**Linux / macOS：**

```bash
nohup ./ds-proxy > proxy.log 2>&1 &
```

**systemd 服务（推荐用于服务器常驻）：**

创建 `/etc/systemd/system/ds-proxy.service`：

```ini
[Unit]
Description=DeepSeek Thinking Proxy
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/ds-proxy
Restart=always
RestartSec=5
Environment="PORT=15722"
Environment="HOST=127.0.0.1"
Environment="DEEPSEEK_HOST=https://api.deepseek.com"

[Install]
WantedBy=multi-user.target
```

启用并启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable ds-proxy
sudo systemctl start ds-proxy
```

## 配置 Claude Code CLI

通过环境变量让 Claude Code 指向代理：

```bash
# Linux / macOS
export ANTHROPIC_BASE_URL="http://127.0.0.1:15722/anthropic"
export ANTHROPIC_API_KEY="sk-你的DeepSeek密钥"
claude

# Windows PowerShell
$env:ANTHROPIC_BASE_URL = "http://127.0.0.1:15722/anthropic"
$env:ANTHROPIC_API_KEY = "sk-你的DeepSeek密钥"
claude
```

## 核心修复逻辑

```
Claude Code 发起请求
      │
      ▼
┌─────────────────┐
│ ds-proxy │ ← 拦截请求，解析 messages 数组
│   (本代理)        │
└─────────────────┘
      │
      ▼
检查每个 role="assistant" 的消息：
  ├─ content 为数组（Anthropic 格式）
  │   └─ 若无 type="thinking" 块 → 头部插入空块
  │      {type: "thinking", thinking: "", signature: ""}
  │
  └─ content 为字符串（OpenAI 兼容格式）
      └─ 若无 reasoning_content 字段 → 补空字符串 ""
      │
      ▼
转发至 DeepSeek API → 正常响应（无 400 错误）
```

## 常见问题

### Q: 代理启动后 Claude Code 仍然报错？

A: 请确认：
1. 代理进程正在运行（`curl http://127.0.0.1:15722` 应能连通）
2. Claude Code 的 Base URL 已正确指向代理地址
3. 环境变量设置后已**完全重启** Claude Code 终端

### Q: 是否支持其他模型？

A: 本代理仅针对 DeepSeek V4 系列的 Thinking 模式做了修复。对于不返回 `thinking` 字段的模型（如 `deepseek-chat`），代理会原样透传，不会产生副作用。

### Q: 是否支持流式响应？

A: 完全支持。代理使用 `io.Copy` 零拷贝透传 SSE 流式响应，不会破坏 Claude Code 的实时打字效果。

### Q: 性能开销如何？

A: 代理仅做轻量级 JSON 解析和字段注入，CPU 和内存开销极低。在本地测试环境下，单次请求延迟增加 < 1ms。

## 技术栈

- **语言**: Go 1.21+
- **依赖**: 零外部依赖，纯标准库
- **协议**: HTTP/1.1（支持 SSE 流式传输）

## 相关链接

- [DeepSeek API 文档](https://platform.deepseek.com/api-docs)
- [Claude Code 官方文档](https://docs.anthropic.com/en/docs/agents-and-tools/claude-code/overview)

## License

MIT License — 详见 [LICENSE](LICENSE) 文件。
