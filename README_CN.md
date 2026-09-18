# CodexProxy

[English](README.md) | 中文

CodexProxy 是 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的精简 fork。它继续提供 OpenAI、Codex 和 Claude 兼容 API，但原生上游只保留 Codex 与 Claude。

## 支持范围

| 上游 | 凭据方式 | 下游接口 |
| --- | --- | --- |
| Codex | OAuth、设备码 OAuth、Codex-compatible API Key | OpenAI Chat Completions、Responses、Codex 直连别名、图片、Realtime |
| Claude | OAuth、Claude-compatible API Key | Claude Messages，以及经过翻译的 OpenAI 兼容接口 |
| 通用兼容网关 | `openai-compatibility`、插件或 SDK executor | OpenAI Chat Completions 和 Responses 兼容接口 |

Gemini、Interactions、Vertex、AI Studio、Antigravity、Kimi、xAI 和 Grok 不属于当前原生运行范围。分阶段清理期间，仓库中可能暂时保留这些平台的历史实现，但精简版服务器不会再注册它们的 CLI 参数、HTTP 路由、管理接口、基线 executor、凭据或模型。

## 核心能力

- OpenAI 兼容的 `/v1/chat/completions`、`/v1/completions`、`/v1/responses` 和 `/v1/models`
- Claude 兼容的 `/v1/messages` 与 `/v1/messages/count_tokens`
- `/backend-api/codex` 下的 Codex 直连路由别名
- 流式、非流式及受支持的 Responses WebSocket 请求
- 工具调用、文本/图片输入和 Codex 图片生成
- 多凭据普通轮询、加权轮询或 fill-first 调度
- 重试、冷却、故障切换、优先级、前缀、模型别名及可选会话粘滞
- 配置与凭据热更新
- 可选 Management API、TUI、插件和可嵌入 Go SDK
- 文件、Postgres、Git 和对象存储后端

## 快速启动

要求 Go 1.26 或更高版本。

```bash
cp config.example.yaml config.yaml
# 启动前必须替换示例 api-keys。
go run ./cmd/server --config config.yaml --local-model
```

模板使用 `8317` 端口；仅供本机使用时建议设置 `host: "127.0.0.1"`。健康检查：

```bash
curl http://127.0.0.1:8317/healthz
```

配置好上游凭据后查询模型：

```bash
curl http://127.0.0.1:8317/v1/models \
  -H 'Authorization: Bearer YOUR_PROXY_API_KEY'
```

## OAuth 登录

```bash
go run ./cmd/server --config config.yaml --codex-login
go run ./cmd/server --config config.yaml --codex-device-login
go run ./cmd/server --config config.yaml --claude-login
```

需要时可增加 `--no-browser` 或 `--oauth-callback-port <port>`。

## OpenAI-compatible 网关

第三方平台统一通过兼容配置接入，不再增加原生 Provider：

```yaml
openai-compatibility:
  - name: "example"
    base-url: "https://gateway.example.com/v1"
    api-key-entries:
      - api-key: "sk-..."
    models:
      - name: "upstream-model-id"
        alias: "example-model"
```

路由、重试、模型别名和 Provider 配置参见 [config.example.yaml](config.example.yaml)。

## 代码结构

- `cmd/server/`：进程入口、CLI 参数、配置与存储初始化
- `internal/api/`：Gin 路由、中间件和 Management API
- `sdk/api/handlers/`：OpenAI、Claude 的统一请求处理
- `sdk/cliproxy/auth/`：凭据生命周期、选择、重试与冷却
- `internal/runtime/executor/`：上游执行器
- `internal/translator/`：协议转换注册表与实现
- `internal/thinking/`：统一 reasoning 配置及 Provider 翻译
- `internal/registry/`：动态模型注册与模型目录
- `sdk/cliproxy/`：可嵌入服务生命周期与热更新

## 开发验证

上游审查位置、已发布补丁、提交状态和暂缓项目参见[上游同步台账](docs/upstream-sync.md)。

```bash
gofmt -w .
go test ./...
go build -o test-output ./cmd/server && rm test-output
```

## 许可证

本项目沿用上游仓库许可证，参见 [LICENSE](LICENSE)。
