# M3-1 manifest とダウンローダ — G0 設計比較(承認済み)

- 日付: 2026-07-10
- 手順: Fable 案(A)と Codex 独立案(B、design_alternative ブラインド)を比較し、統合案を人間に提案する
- ステータス: **承認済み(G0、下記 D の条件を含む)**

## A. Fable 案

### manifest

```go
// internal/model
type Info struct {
    Name   string // "large-v3-q5_0" など(ファイル名・URL・API の name はこれのみ)
    URL    string // pinned HF URL
    SHA256 string
    Size   int64
}
var Manifest = map[string]Info{...} // コード内定数。whisper 2 種 + silero VAD
```

- API の `{name}` は必ず manifest キーと突合(存在しない name は 404)→ パストラバーサル根絶(ユーザー入力をパスに使わない)
- 保存ファイル名は manifest 由来の固定名。`os.UserConfigDir()/whisper-cpp-gui/models/{name}.bin`

### ダウンローダ

- `Download(ctx, name, progress func(done, total int64)) error`
- `http.Client` GET → `{name}.bin.partial` へ書き込み。`io.MultiWriter(file, sha256)` でハッシュ同時計算(2 パス不要)
- 完了時: サイズ・SHA256 検証 → 一致なら `os.Rename(.partial → .bin)`(原子的)、不一致なら .partial 削除 + エラー
- ctx キャンセル / ネットワーク断 / ENOSPC: .partial 削除
- 並行制御: モデル名ごとの mutex。DL 中の再要求は「進行中」を返す(二重 DL しない)
- 起動時: models/ の `*.partial` を掃除(M1-5 の起動時掃除と同輪郭)
- `List()`(取得済み一覧 + サイズ)、`Delete(name)`(manifest 突合後に削除)

### テスト

- httptest フェイク HF サーバー: 正常 / SHA256 不一致 / 途中切断(Flush 後にコネクション切断)/ 巨大 Content-Length
- ctx キャンセルで .partial が消えること
- 並行 Download 呼び出しで 1 本しか走らないこと
- Delete 後に List から消え「未取得」扱いになること(M3-2 の前提)

## B. Codex 独立案(gpt-5.5 xhigh, design_alternative ブラインド)

要旨(全文は生ログ参照):

- `Manager` + 固定 manifest。`Download(ctx, name, Reporter)` / `List` / `Delete` / `Path` / `CleanupPartials`。model 層は HTTP サーバー概念なし(A 案と同一の輪郭)
- 進捗は callback(`Reporter(Progress{Phase: preparing|downloading|verifying|complete, Received, Total})`)
- パスは manifest の `Filename` のみから構築(ユーザー入力 name は lookup 専用)+ **manifest 自己検証**(Filename==Base、SHA256 64hex、size>0、絶対 URL)を初期化時に実施
- 同名並行 DL は `ErrAlreadyDownloading`(A 案と同じ「二重 DL しない」)。resume なし、最初からやり直し
- List は SHA256 再計算しない(GB 級で重い)。存在 + サイズ判定
- **DL 中の Delete は busy エラー**(書き込み中ファイルを消さない)
- 既存 final が壊れている場合、明示 Download 時に SHA256 確認 → 不一致なら削除して再取得
- `HTTPDoer` interface + 保存先注入で httptest 完結。テストケース列挙は A 案を包含(+ HTTP 500/404、サイズ不一致、manifest 検証)

## C. 差分と統合提案(Fable)

骨子は完全収束(固定 manifest、lookup 専用 name、.partial + 原子 rename、ハッシュ同時計算相当、二重 DL 拒否、起動時 .partial 掃除)。差分と採否:

| 論点 | A(Fable) | B(Codex) | 統合案 |
|---|---|---|---|
| 進捗の型 | func(done, total) | **Phase 付き Progress struct** | **B 採用**(verifying 表示が可能に) |
| manifest 自己検証 | なし | **初期化時に検証** | **B 採用**(定数のタイポを起動時に検出) |
| DL 中の Delete | 未検討 | **busy エラー** | **B 採用** |
| 壊れた final の扱い | 未検討 | 明示 DL 時に検証→再取得 | **B 採用** |
| List の SHA256 | 未定 | 再計算しない | **B 採用**(性能) |
| HTTP 注入 | http.Client | HTTPDoer interface | **B 採用**(等価だがテスト自由度が高い) |

### 人間への確認事項(統合)

1. `GET /api/models` は「manifest 全件 + downloaded フラグ」を返す(M3-2 の UI が未取得も表示する必要があるため)で良いか — 推奨: はい
2. DL 中の同名再要求は 409(進行中)で良いか、進行中 DL への「途中参加」(進捗共有)が必要か — 推奨: v1 は 409。UI は進行中表示を出す
3. DL キャンセル API は M3 スコープ外(リクエスト ctx キャンセルのみ)で良いか — 推奨: はい
4. 壊れた final は明示 DL 時に自動削除・再取得で良いか — 推奨: はい(SHA256 は改変検知が目的)

推奨: 統合案で承認いただければ M3-1 実装に着手する(SHA256 検証は G4 対象、Fable 自ら実装)。

## D. 人間承認結果(2026-07-10)

統合案を以下の補足条件付きで承認:

1. manifest URL は許可済み HF repository の full commit SHA に固定する。name は lookup 専用、filename は manifest 由来のみ
2. POST は background DL を開始して 202。`GET /api/models/{name}/events` で SSE 進捗を共有し、同名再要求は 409
3. HTTPS redirect は最大 5 回、HTTP downgrade を拒否。redirect URL/query はログへ出さない
4. `Content-Length > manifest size` を事前拒否し、stream 自体も `size+1` byte で停止する
5. process 内 mutex + ランダム partial を使い、size/SHA256 検証後に atomic publish。起動時は 24 時間超の partial だけ cleanup する
6. `GET /api/models` は manifest 全件と state を返す。実行前 integrity 検証を行い、使用中モデル Delete は server 層で 409
7. v1 は個別 DL cancel API を持たず、background context は app shutdown で cancel する
