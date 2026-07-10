# M1-1 サーバー起動と基本セキュリティ — G1/G2/G3/G4 記録

- 日付: 2026-07-10
- 対象: cmd/whisper-cpp-gui/main.go, main_test.go, internal/server/server.go, server_test.go
- G1: verifier PASS(4 コマンド全通過。受け入れ条件 6 項目すべてに対応テストの存在を確認)。加えて `go test -race` クリーン、実バイナリ起動スモークで 401/403 を実測確認
- G2: `codex exec --model gpt-5.5 -c 'model_reasoning_effort="xhigh"' --sandbox read-only --skip-git-repo-check` + `review_blind.md`(ブラインド)

## G2 指摘と G3 分類(全 2 件、blocking なし)

| # | severity | 指摘 | 分類 | 対応 |
|---|---|---|---|---|
| 1 | should-fix | `main.go` の `open` 起動が `Start()` のみで `Wait()` されず zombie 化し得る | **同意** | `cmd.Start()` 成功後に `go cmd.Wait()` で回収するよう修正 |
| 2 | should-fix | `xdg-open` も同様 | **同意** | 同上(共通経路で修正) |

受け入れ条件: Codex 判定で全 6 項目 satisfied。

## G1 再実行

修正後、4 コマンド全 PASS(2026-07-10)。

## G4(対立的レビュー・M1-1+M1-2 合同・2026-07-10)

`codex exec ... --sandbox read-only` + `review_adversarial.md`。Exploitable 3 件・Not exploitable 7 件。

| # | severity | 指摘 | 分類 | 対応 |
|---|---|---|---|---|
| 1 | blocking | token が起動 URL / `open`・`xdg-open` の argv / stdout に露出。同一マシンの別プロセスが `/proc/*/cmdline` から token を奪える | **一部同意 / 人間判断** | stdout の URL 表示は M1-1 受け入れ条件そのもの、クエリ token は SSE 制約の受容リスク(plan §16.7)。argv 経由の露出低減とヘッダ token 限定化は M1-5(state-changing API)で対応予定。「同一ユーザーの別プロセス」を脅威に含めるかは**セキュリティ境界の判断=人間承認事項**(§19.3)。テストで反証不能のため blocking は却下せず G5 へ上げる |
| 2 | blocking | `http.Serve` に `ReadHeaderTimeout`/`IdleTimeout`/`MaxHeaderBytes` 未設定 → ローカル Slowloris | **同意・修正済** | `http.Server` を明示生成し 3 つを設定(10s/120s/1MiB) |
| 3 | blocking | `LoadUserConfig` が `os.ReadFile` で無制限読み込み → 巨大 config でメモリ枯渇 | **同意・修正済** | 通常ファイル限定 + 64KiB 上限 + `io.LimitReader`。超過/ディレクトリを拒否するテスト追加 |

Not exploitable(Codex 確認済み): token bypass 無し(ConstantTimeCompare)、DNS rebinding 無し(完全一致)、CSRF/Origin 厳格、bind は 127.0.0.1 固定、パストラバーサル経路未実装、command injection 無し(引数スライス)、SHA256 経路未実装。

ハードニング提案のうち Host/Origin テーブルへの追加ケース(大文字・末尾ドット・IPv6・127.0.0.2・ポート省略・null Origin・localhost.evil.com・大文字スキーム)は**全件テスト追加済み**で 403 を確認。

### G4 後の G1 再実行

修正 + テスト追加後、4 コマンド全 PASS + `-race` クリーン(2026-07-10)。

### 残る人間承認事項(G5)

- **指摘 1 の受容可否**: token を stdout/argv に載せる設計を、本アプリの脅威モデル(研究室内・1人1台ローカル、同一ユーザー権限)で受容するか。受容しない場合は「自動起動時に token をワンタイム化しフロントで交換する」等の別設計が必要(M1-1 の受け入れ条件「URL を標準出力に表示」との整合も要検討)
- plan §19.4: `internal/server` の認証に触れる本 PR のマージは人間承認

### 人間承認結果(2026-07-10)

同一 OS ユーザー権限で動く悪意ある process を脅威モデル外とする条件で、launcher argv の一時的 token 露出を受容する。ただし現在の query URL/stdout 方式はそのまま承認せず、次を M1-1 hardening として実施する:

- startup token は URL fragment で渡し、SPA が `history.replaceState` で即時除去する
- 通常 API は header token のみ、SSE endpoint だけ query token を許可する
- normal startup stdout は token-free base URL。`--no-browser` または browser launch failure 時だけ bootstrap URL を表示する
- `Referrer-Policy: no-referrer` / `Cache-Control: no-store` を付け、token 非記録テストを追加する
- 上記変更後に G1/G2/G4 を再実行する

## 運用メモ

並行 codex 委譲でスクラッチの `prompt.md` が衝突し、初回 G2 が別 Issue のプロンプトで実行される事故があった(結果は破棄し再実行)。以後、委譲時はタスク固有のユニークなファイル名を指示する。
