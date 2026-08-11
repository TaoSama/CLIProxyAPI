# 部署 Runbook

本文件覆盖 devbox 上 **CLIProxyAPI (cpa)** 及其上游 **traex-api** 的完整部署、冒烟测试与回滚流程。

## 架构

```
client (Claude Code / Codex / OpenAI SDK)
        │
        ▼
┌─────────────────────┐   :8317
│  CLIProxyAPI (cpa)  │ ─────────┐
└─────────────────────┘          │
        │                        │
        │ OpenAI 协议            │ Anthropic / Codex / Gemini 协议
        ▼                        ▼
┌─────────────────────┐   upstream providers
│     traex-api       │   (cfjwlpro / taoai / ...)
│     :18080          │
└─────────────────────┘
        │
        │ Cloud-Cli-Jwt
        ▼
copilot-cn.bytedance.net (TraeX 上游)
```

- **traex-api**：把字节 TraeX 上游翻译成 OpenAI 协议，监听 `:18080`
- **CLIProxyAPI (cpa)**：多协议适配层（Anthropic / Codex / Gemini / OpenAI），监听 `:8317`，上游可以是 traex-api 也可以是外部 provider

## 目录约定（devbox）

| 组件 | 二进制 | 配置 | 日志 |
|------|--------|------|------|
| traex-api | `~/.local/bin/traex-api` | systemd unit 内 `-jwt-path` | `~/Workspaces/traex-api.log` |
| cpa | `~/cliproxyapi/cli-proxy-api` | `~/.cli-proxy-api/config.yaml` | `~/.cli-proxy-api/logs/` |

JWT：`~/.traex-jwt-token → ~/.claude/.credentials/tokens/traex_jwt`（每日 03:15 由 `traex-jwt-refresh.timer` 刷新）。

---

## 一、traex-api 部署

### 1.1 构建（本地交叉编译）

```bash
cd ~/Workspaces/traex-api
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /tmp/traex-api ./cmd/traex-api
ls -lh /tmp/traex-api    # ~5.7MB, statically linked ELF
```

### 1.2 备份 + 上传

```bash
ssh devbox 'test -f ~/.local/bin/traex-api && cp ~/.local/bin/traex-api ~/.local/bin/traex-api.bak'
rsync -av /tmp/traex-api devbox:~/.local/bin/traex-api
ssh devbox 'chmod 755 ~/.local/bin/traex-api'
```

### 1.3 重启 + 验证

```bash
ssh devbox 'systemctl --user restart traex-api.service && sleep 2 && systemctl --user is-active traex-api.service'
# 预期: active
```

### 1.4 冒烟测试

```bash
BASE="http://127.0.0.1:18080"

# healthz: JWT 已加载 + 模型数
ssh devbox "curl -sf $BASE/healthz" | jq .
# 预期: {"ok":true,"jwt_loaded":true,"models_registered":<N>}

# 模型列表
ssh devbox "curl -sf $BASE/v1/models" | jq '.data | length'

# 非流式 chat
ssh devbox "curl -sf -X POST $BASE/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{\"model\":\"traex/doubao-seed-2.1-turbo\",\"messages\":[{\"role\":\"user\",\"content\":\"pong\"}],\"max_tokens\":16,\"stream\":false}'" \
  | jq '{model, finish_reason: .choices[0].finish_reason}'
```

### 1.5 回滚

```bash
ssh devbox 'cp ~/.local/bin/traex-api.bak ~/.local/bin/traex-api && systemctl --user restart traex-api.service'
```

---

## 二、CLIProxyAPI (cpa) 部署

### 2.1 构建

在 devbox 上从源码构建（Linux/amd64 原生，避免交叉编译的 CGO 问题）：

```bash
ssh devbox 'cd ~/CLIProxyAPI-verify && go build -o ~/cliproxyapi/cli-proxy-api.new ./cmd/server'
```

或本地交叉编译（如果不依赖 cgo 插件）：

```bash
cd ~/Workspaces/CLIProxyAPI
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/cli-proxy-api ./cmd/server
rsync -av /tmp/cli-proxy-api devbox:~/cliproxyapi/cli-proxy-api.new
```

### 2.2 备份 + 替换

```bash
ssh devbox 'cd ~/cliproxyapi && mv cli-proxy-api cli-proxy-api.bak-$(date +%Y%m%d-%H%M%S) && mv cli-proxy-api.new cli-proxy-api && chmod +x cli-proxy-api'
```

### 2.3 插件构建（claude-web-search-router）

插件是 cgo shared library，必须在目标平台构建：

```bash
ssh devbox 'cd ~/CLIProxyAPI-verify/examples/plugin/claude-web-search-router/go && \
  go build -buildmode=c-shared -o ~/.cli-proxy-api/plugins/linux/amd64/claude-web-search-router.so .'
```

插件目录结构：`~/.cli-proxy-api/plugins/<os>/<arch>/<plugin_id>.so`

### 2.4 配置

主配置：`~/.cli-proxy-api/config.yaml`

关键项：

| 字段 | 说明 |
|------|------|
| `port` | 监听端口，默认 `8317` |
| `auth-dir` | 凭据目录，放 `codex-*.json`、`claude-*.json` 等 |
| `plugins.dir` | 插件目录，指向 `~/.cli-proxy-api/plugins` |
| `commercial-mode` | `true`，禁用高开销中间件，降低高并发内存占用 |
| `disable-claude-cloak-mode` | `true`，关闭 Claude 请求伪装，保护上游 prompt cache |
| `codex.optimize-multi-agent-v2` | `true`，Codex 多 Agent v2 兼容 |

### 2.5 重启 + 验证

```bash
ssh devbox 'systemctl --user restart cli-proxy-api.service && sleep 2 && systemctl --user is-active cli-proxy-api.service'
```

确认日志中插件加载成功：

```bash
ssh devbox 'journalctl --user -u cli-proxy-api.service -n 20 --no-pager' | grep plugin
# 预期: plugin loaded plugin_id=claude-web-search-router
#        plugin registered plugin_id=claude-web-search-router version=0.1.0
```

### 2.6 冒烟测试

```bash
CPA="http://127.0.0.1:8317"

# 模型列表（需要 api-key）
ssh devbox "curl -sf -H 'Authorization: Bearer sk-dummy' $CPA/v1/models" | jq '.data | length'

# OpenAI 协议 chat
ssh devbox "curl -sf -X POST $CPA/v1/chat/completions \
  -H 'Authorization: Bearer sk-dummy' -H 'Content-Type: application/json' \
  -d '{\"model\":\"gpt-5.6-sol\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":16,\"stream\":false}'" \
  | jq '{model, finish_reason: .choices[0].finish_reason}'

# Anthropic 协议 messages
ssh devbox "curl -sf -X POST $CPA/v1/messages \
  -H 'x-api-key: sk-dummy' -H 'anthropic-version: 2023-06-01' -H 'Content-Type: application/json' \
  -d '{\"model\":\"gpt-5.6-sol\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'" \
  | jq '{model, stop_reason}'
```

### 2.7 回滚

```bash
ssh devbox 'cd ~/cliproxyapi && cp cli-proxy-api.bak-<timestamp> cli-proxy-api && systemctl --user restart cli-proxy-api.service'
```

---

## 三、systemd units

### traex-api.service

```ini
[Unit]
Description=TraeX API Wrapper (OpenAI Compatible)
After=network.target

[Service]
Type=simple
Environment=TRAEX_PROXY_LOG_REQUESTS=1
ExecStart=/home/luwentao/.local/bin/traex-api -host 0.0.0.0 -port 18080 -jwt-path /home/luwentao/.traex-jwt-token
Restart=always
RestartSec=3
StandardOutput=append:/home/luwentao/Workspaces/traex-api.log
StandardError=append:/home/luwentao/Workspaces/traex-api.log
KillMode=mixed
TimeoutStopSec=10

[Install]
WantedBy=default.target
```

### cli-proxy-api.service

```ini
[Unit]
Description=CLIProxyAPI - AI CLI Proxy (Anthropic / OpenAI compatible facade for traex-api)
After=network.target traex-api.service

[Service]
Type=simple
WorkingDirectory=/home/luwentao/cliproxyapi
ExecStart=/home/luwentao/cliproxyapi/cli-proxy-api -config /home/luwentao/.cli-proxy-api/config.yaml
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
```

---

## 四、常见运维

```bash
# 看日志
ssh devbox 'journalctl --user -u traex-api.service -f'
ssh devbox 'journalctl --user -u cli-proxy-api.service -f'

# 重启
ssh devbox 'systemctl --user restart traex-api.service cli-proxy-api.service'

# 手动刷新 JWT
ssh devbox 'systemctl --user start traex-jwt-refresh.service'

# 看 timer
ssh devbox 'systemctl --user list-timers | grep traex'
```

## 五、排错速查

| 现象 | 处理 |
|------|------|
| traex-api `jwt_loaded: false` | 检查 `~/.traex-jwt-token` 是否可读，手动 `systemctl --user start traex-jwt-refresh.service` |
| cpa 启动失败 `plugin not found` | 检查 `plugins.dir` 路径是否正确，`.so` 是否在 `<os>/<arch>/` 子目录下 |
| cpa `ErrUnknownModel` | 检查 traex-api 模型 registry，或 cpa config 里的模型别名 |
| cpa 插件加载失败 | `.so` 必须在目标平台构建，不能跨平台拷贝 `.dylib` |
| 客户端 401 | 检查 `api-keys` 配置，或请求头 `Authorization: Bearer <key>` |
