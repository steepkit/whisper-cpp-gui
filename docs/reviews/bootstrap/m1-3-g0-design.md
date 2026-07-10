# M1-3 ジョブ状態機械 — G0 設計比較(承認済み)

- 日付: 2026-07-10
- 手順: Fable 案(下記 A)と Codex 独立案(下記 B、design_alternative ブラインド)を比較し、統合案を人間に提案する
- ステータス: **承認済み(G0、下記 D の条件を含む)**

## A. Fable 案

### 型と状態機械

```go
// internal/job
type Status string // "queued" | "running" | "done" | "failed" | "cancelled"

type Job struct {
    ID         string
    InputPath  string
    OutputDir  string
    Preset     string
    // 以下は mu 保護
    status     Status
    progress   int        // 0-100, best-effort
    logs       *ringBuffer // 上限 N 行(内部定数、例 1000)
    stderrTail *boundedBuf // 固定サイズ(例 8KiB)
    startedAt, finishedAt time.Time
    cancel     context.CancelFunc
}
```

許可遷移は `queued→running`、`queued→cancelled`(実行前キャンセル)、`running→done|failed|cancelled` のみ。それ以外は拒否(プログラミングエラーとして error を返す)。

### 直列キュー

- `Store`(map + RWMutex)と `Queue`(worker goroutine 1 本 + チャネル)を分離
- worker は 1 件ずつ `Runner` を呼ぶ。同時実行 1 はワーカー数 = 1 で構造的に保証
- `Runner` は interface(実体は `internal/exec` のパイプライン)。job 層は `func(ctx, *Job) error` 相当だけ知る

### イベント購読(HTTP 非依存)

- `Subscribe(jobID) (<-chan Event, func())`。Event = `{Type: state|progress|log, ...}`
- 購読時点以降のイベントのみ配信(古いログ再送なし = M1-5 要件を構造で満たす)。現在状態は `Snapshot(jobID)` で取得
- 遅い購読者はドロップ(送信は non-blocking)。SSE 側でスナップショット + 追従に再同期

### キャンセル / タイムアウト

- `context.WithTimeout(12h 内部定数)` + `Cancel(id)` は cancel func 起動
- プロセス終了は exec 層の責務: ctx キャンセル検知で SIGTERM → 猶予(内部定数 5s、テストでは短縮可能に注入)→ SIGKILL
- 終端状態への遷移時に共通 cleanup(`os.TempDir()/whisper-cpp-gui/jobs/{id}` 削除)。キャンセル・タイムアウト・失敗・成功のどの経路でも同一コード

### テスト戦略

- 状態遷移: テーブル駆動ユニット
- cancel: スタブ whisper-cli(STUB_PROGRESS_INTERVAL 長め)を起動し Cancel → プロセス終了 & 一時ディレクトリ消滅を検証
- 直列性: Runner をフェイクにし、同時 running が常に ≤1 をカウンタで検証
- タイムアウト: テスト用に上限を短く注入(内部定数を Config 化しテストからのみ設定)
- ログ上限: 上限超の行を流し、保持行数・バイト数が定数以下に留まることを検証

### 人間への確認事項(Fable 案)

1. タイムアウト時の終端状態は `failed`(reason=timeout)とし、`cancelled` はユーザー操作のみとするで良いか
2. SIGTERM→SIGKILL の猶予は 5 秒の内部定数で良いか

## B. Codex 独立案(gpt-5.5 xhigh, design_alternative ブラインド)

要旨(全文は codex 生ログ参照):

- `Manager` に store / FIFO / cancel / ログ / 購読を一元化。`Runner` interface(`Run(ctx, RunRequest, Observer) error`)と `Observer`(LogStderr / Progress)で exec 層と分離
- 状態遷移は A 案と同一(`queued→cancelled` の実行前キャンセルを明示的に許可)
- 単一 worker goroutine で直列性を構造的に保証(A 案と同じ)
- **timeout は `failed` + `ErrorCode=timeout`**(user cancel と区別。A 案の確認事項 1 と同じ結論)
- cancel は `context.WithCancelCause`(user cancel / timeout を cause で区別)
- **プロセスグループへ SIGTERM → 猶予 → SIGKILL**(bash スタブの子プロセスも確実に落とす)
- ログは行数でなく **byte 上限の ring**(全体 cap + 1 行 cap)+ stderr tail は別の固定 byte ring
- cleanup は job 層が所有し全終端経路で共通。cleanup 失敗は記録し、起動時 stale cleanup(M1-5)で再試行
- slow subscriber は job 実行をブロックしない(切断または overflow event)
- テスト: fake Runner での決定論ユニット + スタブ E2E。**`STUB_IGNORE_TERM=1` をスタブに追加**して SIGKILL 経路を検証する提案

## C. 差分と統合提案(Fable)

両案は骨子が収束(単一 worker、HTTP 非依存購読、終端時共通 cleanup、timeout=failed)。差分と採否:

| 論点 | A(Fable) | B(Codex) | 統合案 |
|---|---|---|---|
| ログ上限 | 行数上限 + tail バイト上限 | **バイト上限 ring + 1 行上限** | **B 採用**(巨大 1 行でもメモリ固定) |
| シグナル送信先 | プロセスへ | **プロセスグループへ** | **B 採用**(スタブは bash+sleep 子プロセスであり実際に必要) |
| cancel 原因の区別 | status のみ | **WithCancelCause + ErrorCode** | **B 採用**(UI が原因を表示できる) |
| cleanup 失敗 | 未検討 | 記録 + 起動時再試行 | **B 採用**(M1-5 の起動時掃除と整合) |
| SIGKILL 経路テスト | 未検討 | STUB_IGNORE_TERM をスタブに追加 | **B 採用**(stub_contract.md も更新) |
| Runner の形 | func 相当 | interface + Observer | **B 採用**(進捗/ログの流し込みが明確) |
| 購読 API | Subscribe + Snapshot | 同等 | 一致 |

### 人間への確認事項(統合)

1. **timeout の終端状態**: 両案とも `failed` + reason=timeout を推奨。承認可否
2. **queued ジョブの cancel 許可**(`queued→cancelled`): 両案とも許可を推奨
3. **上限の初期値**: SIGTERM 猶予 5s / stderr tail 64KiB / ログ ring 512KiB / 1 行 8KiB(いずれも内部定数、テストから注入可能)で良いか
4. **完了済みジョブレコードの保持**: v1 はメモリ保持のみ・削除 API なし(プロセス再起動で消える)とし、上限や削除は必要になったら別 Issue で良いか
5. **cleanup 失敗時の扱い**: 文字起こし自体が成功していれば `done` のまま warning 記録(起動時掃除で再試行)として良いか

推奨: 上記 1〜5 すべて記載どおりで承認いただければ、統合案で M1-3 実装に着手する。

## D. 人間承認結果(2026-07-10)

統合案を以下の補足条件付きで承認:

1. timeout は `failed/error_code=timeout`、ユーザー cancel のみ `cancelled`。queued cancel を許可し、cancel 済み job の Runner は起動しない
2. 12h timeout、5s SIGTERM grace、512KiB log ring、8KiB/line、64KiB stderr tail を初期値とし、テスト注入可能にする
3. subscriber overflow 時は購読を閉じる。log/progress は best-effort、state の正は Snapshot とし、SSE 再接続時に必ず再同期する
4. terminal record は最大 100 件かつ 24 時間、queued job は最大 4 件。上限は active/running job を削除しない
5. cleanup 失敗は文字起こし結果が publish 済みなら done + warning。publish 失敗の recovery は M1-4 の 7 日保持規則に従う
6. `STUB_IGNORE_TERM=1` を stub/contract/test に追加し、process group の SIGKILL fallback を検証する
7. Manager は注入された Runner interface だけを知り、process control は `internal/exec` が所有する
