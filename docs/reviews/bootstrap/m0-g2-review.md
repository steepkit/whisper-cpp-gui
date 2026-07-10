# M0 バッチ(M0-1 / M0-2 / M0-4)— G1/G2/G3 記録

- 日付: 2026-07-10
- 対象: リポジトリ雛形、testdata/stubs/、internal/structcheck、internal/exec/stub_smoke_test.go
- G1: verifier PASS(gofmt / build / vet / test 全通過、受け入れ条件対応テストの存在確認済み)
- G2: `codex exec --model gpt-5.5 -c 'model_reasoning_effort="xhigh"' --sandbox read-only --skip-git-repo-check` + `agent_docs/codex/review_blind.md`。git 未初期化のため diff の代わりに追加ファイル一覧+read-only 直接読取で実施

## G2 指摘と G3 分類(全 5 件)

| # | severity | 指摘 | 分類 | 対応 |
|---|---|---|---|---|
| 1 | should-fix | `testdata/stubs/whisper-cli` が invocation.json / 出力の書き込み失敗を握りつぶし exit 0 | **同意** | `mkdir -p` / `write_invocation` / 各出力 printf に `\|\| exit 1` を追加。read-only ディレクトリで rc=1 を確認 |
| 2 | should-fix | `testdata/stubs/ffmpeg` も同様 | **同意** | 同上(invocation.json / WAV 書き込み失敗で exit 1) |
| 3 | should-fix | structcheck のレイヤ検査が (a) server への規則を持たず (b) サブパッケージ未走査 | **一部同意 / 一部不同意** | (b) 同意: `filepath.WalkDir` による再帰走査へ変更。(a) 不同意: 仕様(AGENTS.md / plan §7)は「server → job → exec の一方向依存」と「job/exec から HTTP 参照禁止」であり、server→exec の下方向 import は違反ではない(M1-2 で `/api/config` がバイナリ発見状態を返すのに必要になり得る)。上方向 import(誰も server を import できない、exec は internal を一切 import できない等)は網羅的に禁止済み |
| 4 | should-fix | UI 文字列チェックが CJK のみで英語直書きを検出できない | **同意** | HTML の生テキストノード(言語不問)と title/alt/placeholder/aria-label 属性リテラルの検出を追加。JS 文字列リテラルの網羅検出は M2-1 の i18n 実装形(ルックアップ API)決定後に強化する |
| 5 | nit | help テストが `--vad` 単体の存在を検証していない | **同意** | `--vad\s` の正規表現チェックを追加 |

## Acceptance criteria への Codex 判定

M0-4「決定論的ハーネス」のみ not satisfied(#3/#4 のバイパス根拠)→ 上記修正で解消。他は全て satisfied。

## G1 再実行

修正後、4 コマンド全 PASS(2026-07-10)。#3 の不同意部分は反論 1 往復の再レビューで確認する(結果は本ファイルに追記)。

## G3 再レビュー結果(反論 1 往復目・2026-07-10)

- 指摘 1〜5: 全件 **resolved** と Codex が判定
- 反論(server→exec の下方向 import 許容): Codex **agree**。根拠: AGENTS.md / plan §7 は一方向依存と job/exec の HTTP 非参照のみを要求し、server→exec を禁止していない。禁止するなら仕様の明文化(人間承認事項)が必要
- 新規指摘: [nit] 属性リテラル検出が double quote のみ → **同意・即修正**(single quote も検出するよう正規表現を拡張済み)
- 残存 blocking: なし → **M0 バッチの G2/G3 クローズ**。最終マージ判断(G5)は人間

実行記録: `codex exec --model gpt-5.5 -c 'model_reasoning_effort="xhigh"' --sandbox read-only --skip-git-repo-check`、トークン 41,572
