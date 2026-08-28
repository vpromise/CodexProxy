# CodexProxy

[English](README.md) | [中文](README_CN.md) | 日本語

CodexProxy は、Codex、Claude、および OpenAI 互換アップストリームに限定した軽量プロキシです。OAuth、API キー、ラウンドロビン、モデルエイリアス、ストリーミング、WebSocket、管理 API を提供します。

## 対応範囲

- Codex OAuth と Codex API キー
- Claude OAuth と Claude API キー
- 設定ファイルで登録する OpenAI 互換エンドポイント
- OpenAI Chat Completions、Responses、Claude Messages の相互変換
- Codex WebSocket、Realtime、Images の対象機能
- ファイル、PostgreSQL、Git、オブジェクトストレージ

この fork では、上記以外のネイティブプロバイダー実装、専用 OAuth、専用 executor、専用 translator、専用管理 API は含まれません。追加の互換サービスは `openai-compatibility` またはプラグイン executor で接続してください。

## クイックスタート

```bash
cp config.example.yaml config.yaml
go run ./cmd/server --config config.yaml
```

ビルドとテスト:

```bash
gofmt -w .
go test ./...
go build -o cli-proxy-api ./cmd/server
```

## 主なエンドポイント

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `GET /v1/responses`（WebSocket upgrade）
- `POST /v1/messages`
- `POST /v1/messages/count_tokens`
- `GET /v1/models`
- `POST /v1/images/generations`
- `POST /v1/images/edits`

設定例と詳細な運用方法は [English README](README.md) または [中文 README](README_CN.md) を参照してください。

## ライセンス

MIT License。詳細は [LICENSE](LICENSE) を参照してください。
