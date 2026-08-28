# 精简管理前端

这是当前 CodexProxy fork 随服务一起发布的管理前端。

## 支持范围

- Codex API Key 与 OAuth 配置
- Claude API Key 与 OAuth 配置
- OpenAI 兼容上游
- 认证文件、配额、日志、精简配置和运行信息

Gemini、Vertex、AI Studio、Antigravity、Kimi、xAI/Grok、插件管理和插件商店均已明确移除。

## 开发命令

```bash
bun install --frozen-lockfile
bun run dev
bun run verify
```

生产构建输出为单文件 `dist/index.html`。

刷新 Go 二进制内嵌面板：

```bash
VERSION=__CLI_PROXYAPI_VERSION__ \
  MANAGEMENT_BUNDLE_OUT_DIR=../internal/managementasset/bundled \
  bun run build
```

发布工作流会重新构建前端，并验证内嵌资产与最新源码完全一致。
