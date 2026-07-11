# implementation_plan.md — whisper-cpp-gui

本書は whisper-cpp-gui の仕様・実装マスタープラン・タスク別受け入れ条件の正である。エージェント運用の詳細は [`agentic_workflow.md`](agentic_workflow.md) を参照。本書と他文書が矛盾した場合は本書を優先し、矛盾自体を人間に報告する。

## 1. Project brief

whisper.cpp の複雑な CLI 操作を隠し、研究室内の Mac ユーザー(非エンジニア含む)がローカル完結で文字起こしできる軽量 GUI を提供する。Go 単体バイナリが `127.0.0.1` に HTTP サーバーを立て、ブラウザを UI として使う。`whisper-cli` / `ffmpeg` は Homebrew 導入済みの子プロセスとして実行する。配布は Homebrew tap のソースビルド(署名・notarize 不要)。

## 2. Goals

- 非エンジニアが「brew install → 起動 → ドラッグ&ドロップ → ダウンロード」だけで文字起こしを完了できる
- 音声データが一切外部に送信されない(ローカル完結が絶対要件)
- 依存最小(Go 標準ライブラリのみ、vanilla JS、ビルドステップなし)で長期保守可能
- localhost サーバーであっても攻撃面(DNS rebinding、パストラバーサル、token なしアクセス)を塞ぐ
- 実物バイナリなしで CI 上で全機能をテストできる(スタブ駆動)

## 3. Non-goals

- クラウド処理・外部 API への音声送信
- ユーザー認証・マルチユーザー・リモートアクセス
- ジョブ履歴の永続化(DB なし、メモリのみ)
- cgo / whisper.cpp C API 埋め込み(子プロセス方式を維持)
- .app バンドル化・署名・notarize
- 詳細 CLI オプションの UI 露出(プリセット中心)
- Windows 対応

## 4. MVP scope

M0〜M4 完了時点を v0.1.0 とする:

- 音声/動画ファイルのアップロード → ffmpeg 変換 → whisper-cli 実行 → txt/srt/vtt ダウンロード
- プリセット 2 種(`ja_fast` / `ja_accurate`)+ VAD
- SSE による進捗・ログ表示、キャンセル
- モデル遅延ダウンロード(HF URL + SHA256 固定 manifest)と管理画面
- 未セットアップ状態の `brew install` 誘導
- Homebrew tap 配布 + CI + 導入手順書(日本語)

M5(実機検証)は人間タスク。8GB マシンでの medium プリセット追加は M5 の結果で判断する。

## 5. Assumptions

- 利用者は macOS (Apple Silicon)、Homebrew 導入済みまたは導入可能
- brew 版 `whisper-cpp` は `whisper-cli` コマンドを提供する。`--vad` / `--vad-model` は実機の `whisper-cli --help` で feature detection し、使えない場合は VAD なし実行にフォールバックする
- 1 人 1 台のローカル起動。同時実行 1 の直列キューで十分
- Whisper モデルは Hugging Face `ggerganov/whisper.cpp`、VAD モデルは `ggml-org/whisper-vad` から取得する。SHA256 で検証するため差し替えは検知できる
- 開発環境に実物の whisper-cli / ffmpeg / macOS はない。テストはすべて `testdata/stubs/` に対して行う

## 6. Tech stack

| 層 | 技術 | 制約 |
|----|------|------|
| サーバー | Go 1.22+ 標準ライブラリのみ | `go.mod` に外部依存を追加しない。ルーティングは `http.ServeMux`(メソッド付き) |
| フロント | vanilla JS + CSS、`embed` 同梱 | フレームワーク・bundler・CDN・WebSocket 禁止 |
| プロセス | `os/exec`(引数スライス渡し) | `sh -c` 禁止 |
| ログ | `log/slog` | |
| テスト | `testing` + `httptest` + スタブ E2E | テーブル駆動推奨 |
| 配布 | Homebrew tap、`std_go_args` ソースビルド | |
| CI | GitHub Actions(ubuntu-latest + macos-14) | |

## 7. Repository structure

```
cmd/whisper-cpp-gui/   # main のみ。配線と起動、ロジック禁止
internal/server/       # HTTP ハンドラ, ルーティング, token/Host 検証, SSE
internal/job/          # ジョブ状態機械, 直列キュー, キャンセル
internal/exec/         # whisper-cli / ffmpeg 探索・実行(interface 化)
internal/model/        # manifest, ダウンロード, SHA256 検証
web/                   # index.html, app.js, style.css, locales/ja.json
config/presets.json
testdata/stubs/        # フェイク whisper-cli / ffmpeg
docs/                  # implementation_plan.md, agentic_workflow.md, notes.md, reviews/
agent_docs/            # エージェント向け詳細資料, Codex プロンプト
docs/adr/              # 重要な設計判断の履歴(上書きせず supersede)
.claude/agents/        # Claude Code subagent 定義
.codex/                # Codex CLI 設定
.github/workflows/
AGENTS.md  CLAUDE.md
```

レイヤ規約: `server → job → exec` の一方向依存。`job` / `exec` から HTTP の概念を参照しない。サーバーは「ヘッドレスエンジン + JSON API」、フロントは「純粋な静的 SPA」として分離し、将来 Tauri sidecar 化できる API 設計を保つ。

## 8. Main components

| コンポーネント | 責務 | 主要な設計判断 |
|---|---|---|
| `server` | ルーティング, token/Host 検証ミドルウェア, SSE 配信, 静的配信 | 通常 API は header token、SSE のみ query token。Host 不一致は 403 |
| `job` | `queued → running → done\|failed\|cancelled` 状態機械, 直列キュー | cancel = process group へ SIGTERM → 猶予 → SIGKILL + 一時ファイル掃除 |
| `exec` | バイナリ探索(設定明示パス > 拡張 PATH)と実行, 進捗 stderr パース | interface 化して将来 whisper-server 方式に差し替え可能に |
| `model` | 固定 manifest, DL(進捗 SSE), SHA256 検証, `.partial` 掃除, 一覧/削除 | 遅延ダウンロード。保存先 `os.UserConfigDir()/whisper-cpp-gui/models/` |
| `web` | D&D, プリセット選択, 進捗バー, ログ, モデル管理, 未セットアップ誘導 | 全文字列 `locales/ja.json` 外部化(`data-i18n`) |

## 9. Data / config model

- **Job**(メモリのみ): `id, inputPath, outputDir, outputs[], preset, status, phase, logs[], progress, errorCode, startedAt, finishedAt`。ログは byte 上限、stderr tail は固定サイズに切り詰める。終端 job は最大 100 件かつ 24 時間だけ保持する
- **presets.json**: `id, name(i18n key), model, vad(bool), outputs[]`。v1 は `ja_fast`(large-v3-turbo-q5_0)/ `ja_accurate`(large-v3-q5_0)
- **model manifest**(コード内定数): `name → {HF URL, SHA256, size}`。silero VAD も同列管理
- **ユーザー設定ファイル**(任意): whisper-cli / ffmpeg の明示パスのみ。場所は `os.UserConfigDir()/whisper-cpp-gui/`
- **出力**: `~/Downloads/whisper-cpp-gui/{元ファイル名}/`、recoverable job work: `os.UserCacheDir()/whisper-cpp-gui/jobs/{id}/`

## 10. CLI / API / interface design

CLI: `whisper-cpp-gui`(起動 + ブラウザ自動オープン)、`--version`。開発・CI・検証用に `--port`(localhost サーバーの固定ポート指定)と `--no-browser`(ブラウザ自動起動の抑止)を提供する。一般ユーザー向け手順では通常触れない。

API:

```
GET  /                          # 静的 UI
GET  /api/config                # プリセット, バイナリ発見状態, モデル取得状態
POST /api/jobs                  # multipart: preset + file + 任意の bounded options JSON
GET  /api/jobs/{id}/events      # SSE
POST /api/jobs/{id}/cancel
GET  /api/jobs/{id}/outputs
GET  /api/download/{id}/{file}
GET  /api/models                # manifest 全件 + state
POST /api/models/{name}/download
GET  /api/models/{name}/events  # モデル DL 進捗 SSE
DELETE /api/models/{name}
```

## 11. Implementation phases

依存: M0 → M1 →(M2 ∥ M3)→ M4 の実装/CI → M5 → M4 の tag/publication。各タスクは Scope と Acceptance criteria を完了条件とする。

| Phase | 内容 | 主担当 | 特記 |
|---|---|---|---|
| M0 | 雛形, スタブ, 実行環境調査, 決定論的ハーネス | M0-1/2/4: エージェント、M0-3: **人間** | M0-3 は M1-4 の仕様に影響。最優先で実施 |
| M1 | サーバー起動/セキュリティ, バイナリ探索, ジョブ機械, パイプライン, SSE/DL | Active Orchestrator 主導(セキュリティ核心部) | M1-1/M1-5 は adversarial review 必須(G4) |
| M2 | i18n 基盤, メイン画面, 進捗表示, 未セットアップ誘導 | 委譲可(worktree で M3 と並行) | |
| M3 | manifest/ダウンローダ, 遅延 DL UI | 委譲可(worktree で M2 と並行) | SHA256 検証は G4 対象 |
| M4 | tap, CI, リリースフロー, 手順書 | Active Orchestrator + 人間(tag/release/public 化) | |
| M5 | 実機検証 | **人間のみ** | medium プリセット判断 |

### M0: 開発基盤

#### M0-1: リポジトリ雛形

Scope:
- `cmd/whisper-cpp-gui/main.go`(Hello World 起動のみ)、`internal/` 各パッケージの空枠、`web/`、`go.mod`(Go 1.22+)、MIT LICENSE、`.gitignore` を作成

Acceptance criteria:
- `go build ./...` が通る
- `go vet ./...` が通る

#### M0-2: フェイクバイナリ(スタブ)

Scope:
- `testdata/stubs/whisper-cli`: 受け取った全引数を JSON で `{出力先}/invocation.json` に記録
- `testdata/stubs/whisper-cli`: `--help` で `--vad` / `--vad-model` を含む help を返す
- `testdata/stubs/whisper-cli`: `-pp` / `--print-progress` 指定時だけ stderr に進捗風の行(`progress = 25%` 等)を出力。間隔は `STUB_PROGRESS_INTERVAL` で上書きでき、既定は 1 秒
- `testdata/stubs/whisper-cli`: `-of` プレフィックスに従って固定内容の txt/srt/vtt を生成し、`--vad` を受理
- `STUB_NO_VAD=1` で VAD 非対応版を再現する。この場合 `--help` は VAD フラグを含まず、`--vad` / `--vad-model` 指定時は非ゼロ終了する
- `testdata/stubs/ffmpeg`: 引数記録 + ダミー wav を出力
- `STUB_FAIL=1` で非ゼロ終了を再現
- `STUB_IGNORE_TERM=1` で SIGTERM を無視し、process group への SIGKILL fallback をテストできる

Acceptance criteria:
- スタブ単体をシェルから叩いて出力ファイルが生成される
- `STUB_FAIL=1` で非ゼロ終了を再現できる
- `whisper-cli --help` 相当の出力で VAD 対応/非対応を切り替えられる
- `STUB_PROGRESS_INTERVAL=10ms` 等で E2E の待ち時間を短縮できる
- `STUB_IGNORE_TERM=1` で grace 経過後の SIGKILL 経路を再現できる

#### M0-3: 実行環境調査(人間タスク・M1-4 実機互換性ゲート)

Scope:
- brew 版 `whisper-cpp` の現行バージョンで `whisper-cli` のコマンド名、`--vad` / `--vad-model`、進捗 stderr、`-of` 出力挙動、ffmpeg 引数を確認し、`docs/notes.md` に記録

Acceptance criteria:
- `docs/notes.md` に実機確認結果が記録されている
- VAD が使えない場合のフォールバック方針(VAD なし実行)が M1 仕様に反映されている

M0-3 未完了でも M1-4 の層分離・スタブ E2E 実装は進めてよい。ただし ffmpeg/whisper-cli 引数は暫定契約であり、M0-3 を消化してスタブと実装を追従するまで M1-4 を最終完了またはリリース可能とは扱わない。

#### M0-4: 決定論的ハーネスと ADR 基盤

Scope:
- 仕様違反をプロンプトだけで防がず、標準ライブラリの Go テストまたは shell script で検出できる形にする
- 外部依存追加、`sh -c` 使用、レイヤ違反(`job` / `exec` から HTTP 参照)、UI 文字列直書き、壊れた docs 参照を検出する方針を定める
- `docs/adr/` を作り、重要決定は status 付き ADR として追加し、上書きではなく supersede で履歴を残す
- Claude Code / Codex の Hook・pre-commit 的フィードバックは導入方針を定める。M0 時点では必須化せず、M1 以降に shell + Go 標準ツール中心で段階導入する

Acceptance criteria:
- `docs/adr/README.md` があり、ADR の status と supersede 方針が説明されている
- 初期 ADR として、旧 `plan.md` 廃止、Go 標準ライブラリのみ、子プロセス方式、localhost セキュリティ境界、Codex sandbox 明示が記録されている。model 固定と初期 localhost token 方針は superseding ADR で履歴を保つ
- M1 以降で追加する構造チェック候補が Testing strategy に明記されている

### M1: コアサーバー

#### M1-1: サーバー起動と基本セキュリティ

Scope:
- `127.0.0.1` のランダムポート bind、起動時 `crypto/rand` token 生成
- 全 `/api/*` で token 検証。通常 API はヘッダ `X-Auth-Token` のみ、SSE endpoint だけ `?token=` を許可
- Host ヘッダが `127.0.0.1:{port}` / `localhost:{port}` 以外なら 403
- `/api/*` の Origin ヘッダを検証する。空 Origin は CLI/curl/一部 GET を考慮して許可し、非空 Origin は `http://127.0.0.1:{port}` / `http://localhost:{port}` のみ許可する
- active HTTP connection は既定 128、header 読み取り 10 秒、idle 120 秒、header 1 MiB を内部上限とし、認証前の FD / goroutine 枯渇を防ぐ
- 起動時 token を含む URL fragment は mode `0700` の一時ディレクトリ内の mode `0600` bootstrap HTML にだけ保存する。`open`(darwin)/ `xdg-open`(linux)の argv には token を含まないファイルパスだけを渡す。通常起動の標準出力は token なし base URL とし、`--no-browser` または起動失敗時も bootstrap ファイルパスだけを表示する
- SPA は fragment から token を取得後、即座に `history.replaceState` で URL から除去する。同一 tab の再読み込み回復に限り、検証済み token と active job ID を origin/tab 単位の `sessionStorage` に保存する。不正 fragment は除去して保存済みの有効 token へフォールバックする。再読み込み時は job の存在確認後に SSE snapshot へ再接続し、存在確認の一時エラーだけを上限付き backoff で再試行する。storage 上の無効値・通常 API の 401・header probe で確認した SSE の 401・存在しない job ID は該当 entry を削除し、storage 利用不能時は現在の page load だけで動作する。`localStorage` と Cookie には保存しない。応答に `Referrer-Policy: no-referrer` と `Cache-Control: no-store` を付ける
- 開発・CI・検証用に `--port` / `--no-browser` を提供

Acceptance criteria:
- token なしの `/api/*` が 401 になる httptest がある
- 偽 Host が 403 になる httptest がある
- 不正 Origin が 403、空 Origin と許可 Origin が通る httptest がある
- bind 先が `127.0.0.1` のみであることをテストまたは起動スモークで確認できる
- `--port` 指定時に固定ポートで起動し、占有済みポートでは明確に失敗するテストまたは起動スモークがある
- connection 上限超過が `net/http` handler 到達前に拒否され、既存 connection 終了後に枠が解放される実通信テストがある
- `--no-browser` 指定時にブラウザ起動処理が呼ばれないテストがある
- 通常 API の query token が拒否され、SSE endpoint だけ query token を許可するテストがある
- token が launcher argv、stdout、リクエストログ、Referer に残らないこと、および bootstrap の permission と終了時 cleanup をテストする
- 同一 tab の再読み込みで token と active job ID を復元し、job snapshot/SSE に再接続できる。不正 fragment からの fallback、一時エラーの上限付き retry、通常 API および SSE probe の 401、storage 上の無効値・存在しない job ID の削除、`localStorage` / Cookie を使わないことをテストする

#### M1-2: バイナリ探索

Scope:
- `internal/exec`: 探索順 = 設定ファイル明示パス > PATH(brew の標準パスを追加した上で)
- whisper-cli / ffmpeg それぞれの発見状態を `GET /api/config` で返す
- 未発見時に UI が `brew install` 案内を出せる情報を返す

Acceptance criteria:
- PATH を操作したテストで探索順が検証されている
- `/api/config` が whisper-cli / ffmpeg の発見状態を返すハンドラテストがある

#### M1-3: ジョブ状態機械と直列キュー

Scope:
- `queued → running → done | failed | cancelled` の状態機械
- メモリ上の job store、同時実行 1 の直列キュー
- キャンセルは実行中プロセスへ `SIGTERM` → 猶予後 `SIGKILL`
- キャンセル・終了時の一時ファイル掃除
- ジョブごとの最大実行時間を設ける。v1 既定は 12 時間の内部定数とし、タイムアウト時はキャンセルと同じ掃除経路を通す
- timeout は `failed` + `error_code=timeout`、ユーザー操作 cancel のみ `cancelled` とする。queued job の cancel を許可し Runner は起動しない
- メモリ上のログ ring は 512 KiB、1 行 8 KiB、stderr tail は 64 KiB を初期値とする。終端 job は最大 100 件かつ 24 時間で prune し、queued job は最大 4 件とする
- `Snapshot + Subscribe` を HTTP 非依存で提供する。log/progress event は best-effort、subscriber overflow 時は購読を閉じ、SSE 再接続時に Snapshot から終端状態を含め再同期する
- process group の停止は `internal/exec` が所有する。`exec.CommandContext` の既定即時 Kill に依存せず、SIGTERM → 5 秒 grace → SIGKILL を実装する

Acceptance criteria:
- 状態遷移のユニットテストがある
- cancel 時にスタブプロセスが終了するテストがある
- 同時実行 1 の直列キューがテストされている
- タイムアウト時にプロセス終了と一時ファイル掃除が行われるテストがある
- ログ上限を超えてもメモリ保持量が増え続けないテストがある
- queued cancel race、terminal record prune、queue 上限、slow subscriber overflow 後の Snapshot 再同期テストがある
- `STUB_IGNORE_TERM=1` で process group の SIGKILL fallback が検証されている

#### M1-4: アップロード → パイプライン実行

Scope:
- `POST /api/jobs`: `MultipartReader()` でストリーミング受信し、別 OS ユーザーが先取りできない `os.UserCacheDir()/whisper-cpp-gui/jobs/{id}/` へ保存
- multipart は検証済み preset ID、単一 `file` part、任意の bounded `options` JSON part だけを受理する。`options` は `vad` と allowlist 済み `outputs` の完全指定に限定し、任意 CLI 引数へ変換しない。part 不在・重複・unknown field・不正 preset/options は 400。ユーザー filename は表示用 metadata のみに使い、保存パスには使わない
- アップロードは既定 8 GiB を上限とし、超過時は 413 を返す。値は内部定数として扱い、一般ユーザー向け CLI オプションにはしない
- アップロード本文の読み取り期限は既定 2 時間の内部定数とし、slow upload が単一 upload 枠を無期限に占有しないようにする
- 途中失敗、キャンセル、ディスク不足(`ENOSPC` 等)では `.partial` / 一時ファイルを掃除し、UI に原因を返す
- ffmpeg で 16kHz mono wav 変換
- `whisper-cli` を引数スライスで実行し、進捗取得用の `-pp` と `-of` プレフィックスに従って出力を生成
- VAD 対応ありなら `--vad` / `--vad-model` を付与し、未対応なら VAD なしで実行
- 状態機械は 5 状態を維持し、running 中の `phase=converting|transcribing|moving` で工程を表す。job は orchestration、exec.Engine は CLI 引数・実行・feature detection を所有する
- 出力を `~/Downloads/whisper-cpp-gui/{元ファイル名}/` へ移動する。Downloads が無い場合は `~/whisper-cpp-gui/` へ fallback する
- 出力 publish 失敗時だけ `failed/error_code=output_publish_failed` とし、recovery marker 付き work dir を 7 日保持して UI に path を返す。通常の done/failed/cancelled/timeout は共通 cleanup を通る

Acceptance criteria:
- スタブに対するアップロード → 変換 → 実行 → 出力移動の E2E テストがある
- 100MB 級ダミーファイルでメモリ使用が肥大しないことを検証するテストまたは測定手順がある
- ffmpeg / whisper-cli へ渡す引数がスタブの `invocation.json` で検証されている
- アップロード上限超過が 413 になり、一時ファイルが残らないテストがある
- slow upload の期限超過後に upload 枠と一時ディレクトリが解放される実通信テストがある
- 既定 job root が private な user cache 配下にあり、共有 temp の固定名を先取りされても影響を受けないテストがある
- 変換・実行失敗時に一時ファイルが掃除され、stderr tail が返るテストがある
- 単一 file part/preset/filename sanitize、phase、Downloads fallback、publish 失敗時 recovery と 7 日 cleanup のテストがある

#### M1-5: SSE とダウンロード API

Scope:
- `GET /api/jobs/{id}/events`: ログ行・進捗%・状態変化を SSE 配信
- EventSource 制約により SSE はクエリ token で認証
- subscriber overflow・再接続時は Snapshot を最初に送り、終端状態を取りこぼさない
- SSE のログイベントは保持上限を超えた古いログを再送しない
- `GET /api/download/{id}/{file}`: Job に記録済み output filename だけを final output directory から配信し、`filepath.Clean` + final output directory 配下検証を行う
- 起動時に古い一時ジョブディレクトリを掃除する

Acceptance criteria:
- パストラバーサル(`../../etc/passwd` 等)拒否テストがある
- SSE の受信テストがある
- token なしまたは不正 token の SSE/API アクセスが拒否されるテストがある
- 起動時 cleanup が古い一時ディレクトリだけを削除し、7 日未満の recovery dir を保持するテストがある

### M2: フロント UI

#### M2-1: i18n 基盤と画面骨格

Scope:
- vanilla JS、`web/locales/ja.json` + `data-i18n` 属性で全文字列を外部化
- `en.json` を足せる構造を保つ
- `embed` で Go バイナリに同梱
- ja.json キーは `screen.element.state` 形式、補間は `{model}` / `{size}` の named placeholder に統一
- UI は将来アクセシビリティツリーで検証できるよう、主要操作に label / role / name があるセマンティック HTML を使う

Acceptance criteria:
- `ja.json` の 1 キー変更だけで表示が変わる
- 表示文字列が HTML/JS に直書きされていないことを静的チェックまたはテストで検出できる
- 主要操作にアクセシブルな name があることを静的チェックまたはヘッドレス確認手順で検証できる

#### M2-2: メイン画面

Scope:
- ドラッグ&ドロップ + ファイル選択ボタン
- プリセットのラジオ(高速/高精度)
- 無音スキップと出力形式のチェックボックス
- preset の VAD/outputs を既定値とし、UI が選択した実効値を `options` JSON として送信する。server は同じ allowlist で再検証する
- 開始/キャンセルボタン

Acceptance criteria:
- スタブ環境でアップロード → 進捗表示 → 完了 → DL リンクが通る
- 可能な範囲で API/stub E2E と静的 DOM/i18n チェックに寄せ、手動確認はブラウザ描画や実機依存の部分に限定して PR 説明に記載されている

#### M2-3: 進捗・ログ表示

Scope:
- SSE 購読、進捗バー、折りたたみ式ログ
- 失敗時は ffmpeg / whisper の stderr 末尾を表示
- 進捗パース失敗時も「実行中」表示は維持

Acceptance criteria:
- スタブの `STUB_FAIL=1` でエラー表示が出る
- 進捗 SSE を受けて UI が更新されることを確認できる

#### M2-4: 未セットアップ状態の誘導

Scope:
- whisper-cli / ffmpeg 未発見時に `brew install ...` のコピー可能なコマンドを表示
- `/api/config` の発見状態に応じて表示を切り替える

Acceptance criteria:
- `/api/config` の状態に応じて画面が切り替わる
- 未セットアップ時に必要な brew コマンドが表示される

### M3: モデル管理

#### M3-1: manifest とダウンローダ

Scope:
- `internal/model`: モデル名 → HF URL + SHA256 + サイズの固定 manifest
- Whisper モデル: `ggerganov/whisper.cpp` の `large-v3-q5_0` / `large-v3-turbo-q5_0`
- VAD モデル: `ggml-org/whisper-vad` の silero VAD
- 保存先 `os.UserConfigDir()/whisper-cpp-gui/models/`
- manifest URL は `https://huggingface.co/` の許可 repository と full commit SHA に固定する。name は lookup 専用、保存 filename は manifest 由来のみ
- `POST /api/models/{name}/download` は background download を開始して 202 を返す。`GET /api/models/{name}/events` で進捗を SSE 配信し、同名 DL 中の再要求は 409 とする
- 既に検証済みのモデルへの `POST` は冪等に 202、存在しないファイルへの `DELETE` は冪等に 204 とする。`GET /api/models` は `{"models": [...]}` envelope を返し、source URL とローカルパスを公開しない
- HTTPS redirect は最大 5 回、HTTP downgrade を拒否する。redirect chain は許可済み HF URL から開始し、URL/query をログへ出さない
- モデル取得用 HTTP client は環境変数の proxy を使用しない。全体 12 時間、受信無進捗 2 分を内部 timeout とし、進捗通知は 1 MiB 単位にまとめる
- `Content-Length` が manifest size を超える応答を事前拒否し、長さ不明・虚偽の場合も `size+1` byte で即 abort する
- ランダム化した `.partial` へ書き、サイズ + SHA256 検証後に同一 directory へ atomic publish する。途中失敗時は対象 partial を削除し、起動時 cleanup は 24 時間超の partial だけを対象とする
- `GET /api/models` は manifest 全件と `missing|downloading|downloaded|invalid` state を返す。一覧取得では multi-GB ファイルを毎回 hash せず metadata を確認し、明示的な再 download 判断と実行直前にはサイズ + SHA256 を再検証する。queued/running job が使用中のモデル削除は server 層で 409 にする

Acceptance criteria:
- `httptest` のフェイク HF サーバーに対する DL テストがある
- SHA256 一致 / 不一致テストがある
- 中断時に `.partial` が削除されるテストがある
- redirect 上限/HTTPS downgrade、HTTP status、Content-Length/stream size 上限、同名並行 DL、複数 app 相当の partial 衝突、使用中 Delete のテストがある
- モデル API の認証、冪等 status、path-free response、snapshot-first SSE、queued/running job の lease 保持を handler test で確認する

#### M3-2: 遅延ダウンロード UI

Scope:
- プリセット選択時に必要モデル未取得なら「モデルが必要です [ダウンロード]」を表示
- 取得済みなら即実行可
- モデル管理画面で取得済み一覧・サイズ・個別削除を提供

Acceptance criteria:
- モデル無し状態から DL → 文字起こし開始までがスタブで通る
- モデル削除後に未取得状態として扱われる

### M4: 配布

#### M4-1: homebrew-tap リポジトリ

Scope:
- `steepkit/homebrew-tap` を作成
- `Formula/whisper-cpp-gui.rb`
- `depends_on "go" => :build`、`depends_on "whisper-cpp"`、`depends_on "ffmpeg"`、`std_go_args` でソースビルド

Acceptance criteria:
- `brew install steepkit/tap/whisper-cpp-gui` が macOS ランナー上で成功する
- `whisper-cpp-gui --version` が動く

#### M4-2: CI

Scope:
- 本体リポジトリ: ubuntu-latest で `go vet` + `go test ./...`(スタブ E2E 含む)
- 本体リポジトリ: macos-14 でビルド + 起動スモーク
- tap リポジトリ: `brew install --build-from-source` + `brew test`

Acceptance criteria:
- 両ワークフローがグリーン

#### M4-3: リリースフロー

Scope:
- 本体に git tag → GitHub Release(ソース tarball) → tap formula の `url` / `sha256` 更新手順を docs 化
- v1 は手動リリースで可

Acceptance criteria:
- v0.1.0 タグから利用者が `brew install` できる状態

#### M4-4: 手順書

Scope:
- README(開発者向け・英語可)
- 研究室向け導入手順(日本語)
- 導入手順: `brew install steepkit/tap/whisper-cpp-gui` → 起動 → モデル DL → 文字起こし
- SSE query token の受容リスクと、`127.0.0.1` bind / Host・Origin 検証 / token をログに出さないことを Security note に明記

Acceptance criteria:
- 手順書だけ読んで新規 Mac で導入できる構成
- 実検証が必要な項目は `docs/notes.md` に残っている

### M5: 実機検証(人間タスク)

通常は final tag 前の必須ゲートとする。`v0.1.0` だけは、物理 Mac を所有せず利用機会もないという owner の明示判断により [ADR 0013](adr/0013-waive-physical-mac-validation-for-v0.1.0.md) の release exception を適用する。これは M5 合格ではなく未検証リスクの受容であり、release notes と利用者文書で開示する。GitHub-hosted macOS CI は packaging gate の代替に限り、実 workload / 8GB memory / 権限 / browser launch / 実モデル DL の検証を代替しない。

Scope:
- 研究室 Mac で実ファイル(講義録音など)の文字起こし
- brew 版 whisper-cli の実挙動確認
- Apple Silicon 8GB マシンでの large-v3 メモリ挙動確認
- `~/Downloads/whisper-cpp-gui/` への出力と権限
- `open` によるブラウザ自動起動の実挙動
- HF からのモデル DL 実測(サイズ・速度・SHA256 一致)

Acceptance criteria:
- `docs/notes.md` の M5 項目が消化されている
- 8GB マシンで厳しい場合は medium プリセット追加の判断が記録されている

`v0.1.0` exception:
- `docs/notes.md` の項目を未検証のまま残し、owner waiver、medium を追加しない判断、残余リスクを記録する
- main の macOS build/startup CI と tap の clean macOS source-install/`brew test` CI を通す
- 実機検証済みとは表現せず、物理 Mac で判明した問題は patch release で扱う

## 12. Testing strategy

1. **ユニット**: 状態機械・探索順・manifest 検証・パス検証・リソース上限・retention/prune をテーブル駆動で
2. **ハンドラテスト**: `httptest` で認証(401)、Host 検証(403)、パストラバーサル拒否、SSE 受信
3. **スタブ E2E**: `testdata/stubs/` の whisper-cli / ffmpeg(引数を `invocation.json` に記録、`STUB_FAIL=1` で異常系)に対し、アップロード → 変換 → 実行 → 出力移動の全経路。100MB 級ダミーでメモリが肥大しないこと
4. **モデル DL**: `httptest` フェイク HF サーバーで DL・SHA256 不一致・中断・redirect/downgrade・size cap・`.partial` 掃除
5. **構造チェック**: 外部依存追加、`sh -c` 使用、レイヤ違反、UI 文字列直書き、壊れた docs 参照を Go テストまたは shell script で検出する。LLM レビューを主判定にしない
6. **UI E2E**: M2 では API/stub E2E と静的 i18n/DOM チェックを優先する。アクセシビリティツリー確認は npm/bundler を本体依存にせず、必要なら開発・検証用手順として限定する
7. **CI**: ubuntu-latest で vet + test(E2E 含む)、macos-14 でビルド + 起動スモーク。tap 側は `brew install --build-from-source` + `brew test`
8. **実機でしか確認できない項目**はコード内 TODO ではなく `docs/notes.md` に検証項目として蓄積し、M5 で消化

## 13. Verification commands

変更完了の必要条件(全エージェント共通、AGENTS.md にも記載):

```
gofmt_out="$(gofmt -l .)" && test -z "$gofmt_out"
go build ./...
go vet ./...
go test ./...
```

UI 変更時は加えて「スタブ環境での手動確認手順」を PR 説明に記載する。

## 14. Security / privacy boundaries

- bind は `127.0.0.1` のみ。`0.0.0.0` 禁止
- 起動時 `crypto/rand` token を全 `/api/*` で検証。通常 API は header-only、SSE だけクエリ token(EventSource 制約)。起動 bootstrap は private な mode `0700` directory / mode `0600` HTML 経由で URL fragment を渡して即時除去し、token を launcher argv・stdout・ログ・Referer に出さない。同一 tab の reload 回復用 token / active job ID だけを `sessionStorage` に置き、`localStorage` / Cookie は使わない
- Host ヘッダ検証(DNS rebinding 対策)、`/api/*` への Origin 検証(空 Origin は許可、非空 Origin は同一 localhost origin のみ許可)
- active HTTP connection、header/body read deadline、idle deadline、header byte 数に有限上限を設ける
- ダウンロード API は Job に記録済み filename の allowlist + `filepath.Clean` + final output directory 配下検証
- アップロードは `MultipartReader()` ストリーミング。既定 8 GiB、本文読み取り 2 時間を上限とし、メモリに載せない
- 子プロセスはシェル経由禁止、引数スライス渡し
- 外部通信はモデル DL(pinned HF URL + SHA256)のみ。テレメトリなし。音声・文字起こし結果は一切送信しない
- ジョブ時間(既定 12 時間)・queue・terminal record・ログ保持・stderr tail に上限を設ける
- 一時ファイルはジョブ終了・キャンセル・失敗時に削除し、起動時にも古い一時ジョブディレクトリを掃除する。出力 publish 失敗時の recovery dir だけ 7 日保持する
- recoverable job root は user-owned cache 配下に置き、共有 temp の予測可能な固定名を使わない。一回限りの token bootstrap は private な `os.MkdirTemp()` directory を使う
- モデル DL は full commit-pinned HTTPS URL から開始し、redirect downgrade/回数、応答 byte 数、SHA256 を検証する
- 同一 OS ユーザー権限で動く悪意あるプロセスは脅威モデル外とする。これは token の履歴・ログ残留を許す意味ではなく、fragment bootstrap、即時 URL 除去、header-only API で露出を最小化する

## 15. Risks

| リスク | 影響 | 対策 |
|---|---|---|
| brew 版 whisper-cli のコマンド名 / VAD フラグが想定と異なる | M1-4 の呼び出し仕様が崩れる | M0-3 を最優先実施。VAD なしフォールバックを仕様化。`exec` interface 化で吸収 |
| whisper-cli の stderr 進捗フォーマット変更 | 進捗バーが死ぬ | パース失敗時も「実行中」表示は維持(進捗は best-effort)。スタブに実フォーマットを写経 |
| 8GB マシンで large-v3 が厳しい | 主用途が使えない | M5 で判定 → medium プリセット追加 |
| HF のモデル URL 変更 / 消失 | DL 不能 | SHA256 で検知。manifest 更新はパッチリリースで対応 |
| スタブと実物の乖離 | 実機で初めて壊れる | M0-3 / M5 の知見を `docs/notes.md` → スタブへ反映するループを回す |
| プロンプト指示だけでは制約違反を防げない | 依存追加・レイヤ違反・i18n 漏れが混入する | M0-4 で決定論的ハーネスを設計し、CI ではテスト/構造チェックを主判定にする |
| ローカル利用でも巨大ファイルや長時間ジョブで詰まる | ディスク・メモリ・CPU を使い切る | アップロードサイズ、ジョブ時間、ログ保持、一時ディレクトリ cleanup を仕様化する |
| 複数のアプリ instance が同じモデルを DL する | 固定 `.partial` が衝突・破損する | process 内 mutex + ランダム partial + 検証後 atomic publish。古い partial のみ cleanup |
| Linux で `~/Downloads` が無い | 出力先エラー | 存在確認 + `~/whisper-cpp-gui/` へのフォールバックを仕様化 |
| macos-14 ランナーのコスト/可用性 | CI 不安定 | ubuntu で主要テスト、macos はビルド + スモークに限定 |
| v0.1.0 を物理 Apple Silicon 未検証で公開する | 実 workload、8GB memory、権限、browser launch、実モデル DL の問題が利用者環境で初めて判明する | ADR 0013 の version-specific waiver、利用者/Release 警告、clean macOS packaging CI、patch release で対応 |
| private 開発履歴と public 配布 tree が乖離する | review 未実施の内容を配布する | ADR 0014 に従い private final commit と public root commit の Git tree hash を一致させ、public CI を再実行する |

## 16. Resolved decisions / human tasks

以下は本書作成時点の決定事項として扱う。

1. 旧 `plan.md` の内容は本書へ統合済みであり、`plan.md` は置かない。仕様・マイルストーン・受け入れ条件の正は `docs/implementation_plan.md`
2. brew 版 `whisper-cpp` は `whisper-cli` 前提で設計する。ただし `--vad` / `--vad-model` は実機の `whisper-cli --help` で feature detection し、未対応なら VAD なし実行にフォールバックする
3. Whisper モデル URL は `ggerganov/whisper.cpp`、VAD モデル URL は `ggml-org/whisper-vad` を前提に manifest を作る
4. Codex は `codex exec` を主経路とし、task risk に応じて model / reasoning effort を orchestrator が選ぶ。G0/G4・セキュリティ・rescue は frontier model の high/xhigh、通常調査・定型作業は必要十分な設定とする。実行ごとに model/effort をログへ記録し、`--sandbox read-only|workspace-write` は必ず明示する。profile は補助であり、安全境界を profile 解決に依存しない
5. Codex レビュー結果は G1 後に draft PR を作って PR コメントを正とする。GitHub 未初期化の bootstrap 期だけ `docs/reviews/bootstrap/` に一時ログを残す。`docs/reviews/` のその他の用途は、長期保存したい設計レビューに限定する
6. `--port` / `--no-browser` は v1 に含める。ただし一般ユーザー向け機能ではなく、開発・CI・検証用オプションとして扱う
7. SSE のクエリ token 方式は EventSource 制約による受容リスクとして README の Security note に明記する
8. `ja.json` のキー命名は `screen.element.state` 形式、補間は `{model}` / `{size}` 形式に統一する
9. 重要な設計判断は `docs/adr/` に ADR として残す。ADR は上書きではなく supersede で履歴を保全する
10. Hook / pre-commit 的な自動フィードバックは、shell + Go 標準ツール中心の最小構成から段階導入する。本体依存や npm/bundler は導入しない
11. ジョブタイムアウトは v1 既定 12 時間の内部定数とする。GUI から変更可能にする場合は、ユーザー設定ファイルのスキーマ拡張を伴う別タスクとして扱う
12. terminal job は最大 100 件かつ 24 時間、queued job は最大 4 件。subscriber overflow は切断 + Snapshot 再同期とする
13. M1-4 は M0-3 未完了でもスタブベースで暫定実装できるが、実機確認反映前に最終完了としない
14. token bootstrap は private bootstrap file + fragment + 即時 URL 除去とし、launcher argv / stdout には token を出さない。同一 tab の reload 回復に限って検証済み token と active job ID を `sessionStorage` に保存する。不正 fragment は保存済み有効 token へフォールバックし、401 は通常 API または SSE error 後の header probe で確認して session entry を削除する。`localStorage` / Cookie は使わない。通常 API は header-only、SSE のみ query token。同一 OS ユーザー権限の悪意ある process は脅威モデル外とする
15. 初回 GitHub repository は AI orchestrator が private で作成してよい。public 化は別途人間承認を要する
16. recoverable job work は `os.UserCacheDir()/whisper-cpp-gui/jobs` を既定とし、共有 temp の固定名先取りを防ぐ。一回限りの bootstrap だけ `os.MkdirTemp()` を使う
17. active HTTP connection は v1 既定 128 の内部定数とし、上限超過 connection は handler 到達前に閉じる
18. `v0.1.0` は物理 Apple Silicon M5 を未検証のまま公開する owner waiver を適用する。M5 合格とは扱わず、ADR 0013、`docs/notes.md`、利用者文書、release notes にリスクを明記する。GitHub macOS CI は packaging 代替に限定する
19. `v0.1.0` の private 開発 repository と tap は履歴ごと private archive に残す。canonical public repository は reviewed final tree から単一 root commit として作り、private/public の Git tree hash 一致と public CI を必須にする。詳細は ADR 0014

M0-3 は 2026-07-10 に Ubuntu + Linuxbrew の実物 `whisper-cli` / `ffmpeg` で完了した。確認結果と macOS / 実モデルで残る検証範囲は `docs/notes.md` と `agent_docs/whisper_cli_reference.md` を正とする。

以下は人間タスクとして残す。

1. tap は private 開発 repository で Formula/CI をレビューした後、ADR 0014 に従って private archive と同一 tree の単一 commit public `steepkit/homebrew-tap` を作る
2. Codex 実運用前に pre-flight を行う。`codex --version`、`codex doctor --json`、選択 model/effort の利用可否、`--sandbox read-only|workspace-write` の明示、project `.codex/config.toml` の trust/load を確認する。`--profile reviewer/engineer` は対応する profile ファイルがある場合だけ解決確認し、未作成なら省略する
3. M5: `v0.1.0` は ADR 0013 により owner-waived。物理 Mac の検証項目は未完了の post-release backlog として残す

## 17. Agent orchestration plan(概要)

詳細は [`agentic_workflow.md`](agentic_workflow.md)。

- **Active Orchestrator / Chief Engineer** は Claude Code または Codex が担当できる。問題分解、subagent 委譲、独立 peer review、結果統合、マージ判断(提案)を担う。セキュリティ核心部(M1-1, M1-5, M3-1 の検証ロジック)は orchestrator が直接統合・検証する
- Claude Code 利用時は `.claude/agents/`、Codex 利用時は組み込み subagent を同じ verifier/reviewer/engineer 責務へ割り当てる。**subagent を増やすこと自体を目的にしない**
- Codex は独立した peer engineer としてブラインドレビュー、対立的レビュー、代替設計、Issue 実装、rescue、QA、docs review を担う。model/effort は risk-based に選択して記録し、`--sandbox ...` と `agent_docs/codex/` テンプレートを必須とする

## 18. Codex peer review plan(概要)

- **初回レビューは必ずブラインド**: peer reviewer には diff + Issue 本文 + AGENTS.md のみを渡し、orchestrator 側の設計判断・自己評価・レビュー結果を見せない
- ブラインドレビュー後、orchestrator が指摘を「同意/不同意/人間判断」に分類して統合。不同意は 1 往復だけ反論を返し、平行線なら人間へエスカレーション
- セキュリティ核心部の変更は追加で **adversarial review**(攻撃者視点: token バイパス、DNS rebinding、パストラバーサル、リソース枯渇)を必須とする。子プロセス実行に触れる PR も広めに G4 対象とし、M1-2〜M1-4 は原則 G4 を通す
- レビューゲート G0〜G5 の定義は `agentic_workflow.md` §Review gates

## 19. Human approval points

以下は人間の明示的承認なしに進めない:

1. `AGENTS.md` / 本書の仕様変更
2. `go.mod` への依存追加・禁止技術(フレームワーク等)の導入(原則却下前提の相談)
3. セキュリティ境界(§14)の変更・緩和
4. `internal/server` の認証・Host 検証・ダウンロード API に触れる PR のマージ
5. GitHub リポジトリ作成、git tag、リリース、tap フォーミュラ更新
6. 外部サービスへの一切の送信を伴う機能
7. M0-3 / M5(そもそも人間タスク)

## 20. AGENTS.md / CLAUDE.md に反映すべき内容

- **AGENTS.md**(全エージェント共通・短く): 5 大ルール(スタブ駆動 / 依存禁止 / OS 依存局所化 / i18n / セキュリティ)、検証コマンド、完了条件、docs index、人間承認が要る操作の一覧。特定 orchestrator 固有の話は書かない
- **CLAUDE.md**(Claude Code 固有): Claude が active orchestrator の場合の手順、subagent 使い分け、risk-based Codex 呼び出し、ブラインドレビュー規律、レビュー統合ルール、worktree 運用
