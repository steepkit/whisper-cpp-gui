# AGENTS.md — whisper-cpp-gui

このリポジトリで作業する**すべての AI coding agent**(Codex, Claude, その他)への共通指示書。Claude Code のオーケストレーション運用は `CLAUDE.md`(Claude 以外は読まなくてよい)。

## プロジェクト概要

whisper.cpp の CLI 操作を隠す、研究室内配布用のローカル文字起こし GUI。Go 単体バイナリが localhost に HTTP サーバーを立て、ブラウザを GUI として使う。`whisper-cli` / `ffmpeg` は Homebrew 導入済み前提の子プロセス。配布は Homebrew tap のソースビルド。主対象は macOS (Apple Silicon)、Linux でも動くよう OS 依存を抽象化する。

## Docs index

| 文書 | 内容 |
|---|---|
| `docs/implementation_plan.md` | 仕様の正。実装マスタープラン、マイルストーン M0〜M5、タスク別受け入れ条件、セキュリティ境界 |
| `docs/agentic_workflow.md` | エージェント役割分担、レビューゲート、ブランチ戦略 |
| `docs/notes.md` | 実機でしか確認できない検証項目の蓄積(M0-3 / M5 で消化) |
| `docs/adr/` | 重要な設計判断の履歴。上書きせず supersede する |
| `agent_docs/` | エージェント向け詳細資料と Codex プロンプトテンプレート |

## 最重要ルール(5 つ)

### 1. 実物バイナリなしで開発・テストする
この環境に本物の `whisper-cli` / `ffmpeg` / macOS はない。すべてのテストは `testdata/stubs/` のフェイクバイナリに対して行う。スタブは引数を `invocation.json` に記録し固定出力を生成、`STUB_FAIL=1` 等で異常系を再現する。実機でしか確認できない項目はコード内 TODO ではなく `docs/notes.md` に追記する。

### 2. 依存を増やさない
Go は標準ライブラリのみ(`go.mod` に外部依存を追加しない)。ルーティングは Go 1.22+ の `http.ServeMux`(`"POST /api/jobs"` 形式)。フロントは vanilla JS + CSS を `embed` で同梱。**使用禁止**: React/Vue/Svelte、npm/bundler、CDN、Electron、WebSocket、DB、Docker、Python 依存、cgo。必要に見えたら実装せず理由を添えて人間に相談する。

### 3. OS 依存を局所化する
パスをハードコードしない: モデル・設定は `os.UserConfigDir()` 起点、出力は `os.UserHomeDir()/Downloads`(存在確認付き)、再起動後の recovery を伴うジョブ作業領域は `os.UserCacheDir()` 起点。共有 temp の固定名は使わず、一回限りの bootstrap だけ `os.MkdirTemp()` を使う。ブラウザ起動(`open` / `xdg-open`)等の `runtime.GOOS` 分岐は専用の小関数 1 箇所に閉じ込める。

### 4. UI 文字列は必ず外部化する
表示文字列を HTML/JS にハードコードしない。すべて `web/locales/ja.json` にキー定義し、`data-i18n` 属性または JS ルックアップ経由で参照。`en.json` を足すだけで英語化できる構造を保つ。

### 5. セキュリティ要件(localhost でも必須)
- bind は `127.0.0.1` のみ
- 起動時 `crypto/rand` token を全 `/api/*` で検証する。通常 API はヘッダ `X-Auth-Token` のみ、ブラウザ `EventSource` を使う SSE だけクエリ token を許可する
- ブラウザへの初回 token 受け渡しは URL fragment を使い、SPA は取得直後に `history.replaceState` で URL から除去する。静的/API 応答には `Referrer-Policy: no-referrer` と `Cache-Control: no-store` を付ける
- Host ヘッダが `127.0.0.1:{port}` / `localhost:{port}` 以外は 403
- `/api/*` の Origin は、空 Origin または `http://127.0.0.1:{port}` / `http://localhost:{port}` のみ許可
- ファイル配信は `filepath.Clean` 後にジョブディレクトリ配下であることを検証
- アップロードは `MultipartReader()` ストリーミング(全体をメモリに載せない)
- active HTTP connection 数、header/body 読み取り時間、idle 時間、header byte 数を内部定数で制限する
- 子プロセスは `sh -c` 禁止、`exec.Command` に引数スライスで渡す

脅威モデルは DNS rebinding、悪意ある Web ページ、別ユーザー、誤操作、壊れた外部応答を対象とする。同一 OS ユーザー権限で動く悪意あるプロセスは対象外とするが、token の URL・ログ・履歴への残留は最小化する。

## ディレクトリ責務とレイヤ規約

```
cmd/whisper-cpp-gui/   # main のみ。配線と起動、ロジック禁止
internal/server/       # HTTP, ルーティング, token/Host 検証, SSE
internal/job/          # ジョブ状態機械, 直列キュー, キャンセル
internal/exec/         # whisper-cli / ffmpeg 探索・実行(interface 化)
internal/model/        # manifest, DL, SHA256 検証
web/                   # 静的 SPA + locales/ja.json
config/presets.json    # プリセット定義
testdata/stubs/        # フェイクバイナリ
```

依存は `server → job → exec` の一方向。`job` / `exec` から HTTP の概念を参照しない。サーバー側で HTML をレンダリングしない(将来 Tauri sidecar 化を見据え、エンジン + JSON API と静的 SPA を厳密分離)。

## コーディング規約

`gofmt` 準拠。エラーは `%w` でラップし握りつぶさない。ログは `log/slog`。コード内コメント・識別子は英語、UI 文字列は `ja.json`。テストは `httptest` ハンドラテスト + スタブ E2E を必須、テーブル駆動推奨。プロンプト指示だけに頼らず、依存追加・レイヤ違反・`sh -c`・UI 文字列直書き等はテストまたは構造チェックで検出する。

## 検証コマンドと完了条件

作業完了 = 以下がすべて成立していること:

1. `gofmt_out="$(gofmt -l .)" && test -z "$gofmt_out"` が通る
2. `go build ./...` / `go vet ./...` / `go test ./...` が通る
3. 対応タスクの**受け入れ条件**(`docs/implementation_plan.md`)と構造チェックを満たす
4. UI 変更時: スタブ環境での手動確認手順を PR 説明に記載
5. 1 Issue = 1 ブランチ = 1 PR。無関係なリファクタリングを混ぜない

## 人間の承認が必要な操作

- `AGENTS.md` / `docs/implementation_plan.md` の変更
- 依存追加・禁止技術の導入(原則却下)
- セキュリティ境界の変更・緩和
- git tag / リリース / tap フォーミュラ更新 / GitHub リポジトリ操作
- 外部への送信を伴う一切の機能

仕様に曖昧さ・矛盾を見つけたら、勝手に解釈せず選択肢と推奨を添えて質問すること。

## スコープ外(実装しない)

クラウド処理・音声の外部送信 / 認証・マルチユーザー / ジョブ履歴永続化 / cgo / .app 化・署名 / 詳細 CLI オプションの UI 露出 / Windows 対応
