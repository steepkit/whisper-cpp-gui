# M1-2 バイナリ探索 — G1/G2/G3 記録

- 日付: 2026-07-10
- 対象: internal/exec/discover.go, discover_test.go, internal/server の /api/config 追加分, cmd/whisper-cpp-gui の discoverTools 配線
- G1: verifier PASS(4 コマンド全通過、受け入れ条件 2 項目の対応テスト確認済み)。`go test -race` もクリーン
- G2: `codex exec --model gpt-5.5 -c 'model_reasoning_effort="xhigh"' --sandbox read-only --skip-git-repo-check` + `review_blind.md`(ブラインド)

## G2 結果

- Findings: **none**(blocking / should-fix / nit いずれも 0 件)
- Acceptance criteria: 全 satisfied(探索順、/api/config ハンドラテスト、brew 案内情報、ユーザー設定ファイルの場所)
- Reviewed but OK: 外部依存なし / server→exec は許容方向 / /api/config は認証チェーン配下 / shell 不使用 / stateless discovery

## G3

指摘 0 件のため修正なし。クローズ。

## 設計メモ(レビュー外)

- ユーザー設定ファイルは `os.UserConfigDir()/whisper-cpp-gui/config.json`(JSON、キーは whisper_cli_path / ffmpeg_path)とした。仕様はファイル名・形式を定めていないため実装判断。異論があれば G5 で指摘されたい
- G4(対立的レビュー)は M1-1 と合わせてサーバー面全体で実施(m1-1-g2-review.md に追記)
