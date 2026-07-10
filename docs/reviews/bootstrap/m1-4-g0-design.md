# M1-4 アップロード → パイプライン実行 — G0 設計比較(暫定承認済み)

- 日付: 2026-07-10
- 手順: Fable 案(A)と Codex 独立案(B、design_alternative ブラインド)を比較し、統合案を人間に提案する
- ステータス: **暫定承認済み(G0、M0-3 実機互換性ゲート未完了)**
- 前提: M0-3(実機調査)未実施のため、ffmpeg 引数・whisper-cli の進捗形式・-of 挙動はスタブ契約(agent_docs/stub_contract.md)を仮置きとし、M0-3 の結果で追従する

## A. Fable 案

### 層の割り当て

- **server**: `POST /api/jobs` = multipart ストリーミング受信のみ。`http.MaxBytesReader`(8 GiB 内部定数)+ `MultipartReader()` で `os.TempDir()/whisper-cpp-gui/jobs/{id}/input{ext}` へ io.Copy。超過は 413、受信途中失敗は即掃除。受信完了後 job.Submit
- **job**: M1-3 の Manager/worker が Pipeline(Runner 実装)を直列実行
- **exec**: Pipeline = ①ffmpeg 変換 → ②whisper-cli 実行 → ③出力移動、の 3 ステップ。各ステップは `exec.Command`(引数スライス)

### 各ステップ

1. **変換**: `ffmpeg -i {input} -ar 16000 -ac 1 -y {jobdir}/audio.wav`(M0-3 で確定するまでの仮引数)。stderr tail 保持
2. **実行**: `whisper-cli -m {modelPath} -f {wav} -of {jobdir}/out -l ja + preset の outputs(-otxt/-osrt/-ovtt)`。VAD は起動後初回に `whisper-cli --help` を 1 回実行して feature detection(結果キャッシュ)、対応時のみ `--vad --vad-model {sileroPath}`。進捗は stderr の `progress = N%` を best-effort パース(失敗しても実行中表示は維持)
3. **移動**: `~/Downloads/whisper-cpp-gui/{元ファイル名(拡張子除去・サニタイズ)}/` へ `os.Rename`、EXDEV はコピー+削除にフォールバック。`~/Downloads` が無ければ `os.UserHomeDir()` 直下。既存ディレクトリ衝突は ` (2)` 等のサフィックス

### 異常系

- アップロード 413 / 途中切断 / ENOSPC: jobs/{id} を即削除、原因コードを JSON で返す
- ffmpeg / whisper-cli 非ゼロ終了: failed + stderr tail、jobs/{id} 削除
- キャンセル・タイムアウト: M1-3 の共通経路(プロセスグループ SIGTERM→SIGKILL + 掃除)
- 出力移動失敗(権限等): failed + 原因、一時出力は掃除

### テスト

- スタブ E2E: multipart POST → ffmpeg スタブ wav → whisper-cli スタブ出力 → Downloads(テストでは HOME を TempDir に差し替え)移動まで。invocation.json で両バイナリの引数列を厳密検証
- 413: 上限をテスト用に小さく注入し、超過後に jobs/ 配下が空であること
- 100MB 級: ストリーミング検証(アップロード中の HeapAlloc を測る benchmark/測定手順)
- STUB_FAIL=1 / STUB_NO_VAD=1: 失敗経路と VAD フォールバックの引数検証

## B. Codex 独立案(gpt-5.5 xhigh, design_alternative ブラインド)

要旨(全文は生ログ参照):

- **パイプライン(変換→実行→移動)は job 層に置き**、exec 層は高レベル interface `Engine`(`ConvertToWAV` / `DetectWhisperFeatures` / `Transcribe(req, onProgress)`)として CLI 引数の知識を隠蔽
- server 層は multipart 受信のみ: `MaxBytesReader(max+1)` + `MultipartReader` → `workDir/upload.partial` へ io.Copy → 成功時のみ rename → Enqueue → **202 Accepted `{id, state}`**
- 状態を `queued → converting → transcribing → moving → done` に細分化(サブ状態)
- VAD: feature detection を起動時/初回にキャッシュ。preset vad=true かつ検出成功時のみ `--vad`。`vadApplied` をメタデータ記録
- 出力衝突は `name`, `name-2` 採番。`~/Downloads` なしは `~/whisper-cpp-gui/` へ
- **最終出力移動の失敗時は work dir を消さず残す**(データ喪失防止優先)
- 異常系: 413 / 400(part 不在)/ ENOSPC(507 相当)/ モデル未取得(model_missing で failed、プロセス起動せず)等を分類
- テスト戦略は A 案を包含(+ モデル未取得時に invocation.json が作られないこと、Downloads フォールバック、ReadMemStats での測定)

## C. 差分と統合提案(Fable)

| 論点 | A(Fable) | B(Codex) | 統合案 |
|---|---|---|---|
| パイプラインの所属 | exec 層のステップ列 | **job 層 + exec.Engine 高レベル interface** | **B 採用**(whisper-server 差し替え時に Engine 実装ごと交換でき、job は CLI 引数を知らない。仕様の「exec を interface 化」に最も適合) |
| 状態の細分化 | 5 状態のまま | converting/transcribing/moving を追加 | **A 採用(B は不採用)**。M1-3 仕様と G0 統合案は `queued→running→done\|failed\|cancelled` の 5 状態で確定済み。細分は `phase` フィールド(running 中のみ)で表現し、状態機械は仕様どおり維持 |
| upload の .partial + rename | 未検討 | 採用 | **B 採用** |
| レスポンス | 未定 | 202 Accepted {id} | **B 採用** |
| 移動失敗時の work dir | 掃除 | **残す(データ保護)** | **人間判断**(下記 3) |
| VAD モデル未取得 | 未検討 | 要確認 | **人間判断**(下記 4) |
| 出力衝突 | ` (2)` サフィックス | `name-2` 採番 | B 形式で採用(些末) |

### 人間への確認事項(統合)

1. `POST /api/jobs` は 202 Accepted + `{id}` の非同期開始で良いか — 推奨: はい
2. 8 GiB 上限は multipart body 全体への上限(MaxBytesReader)として扱う — 推奨: はい(実装が単純で安全側)
3. **最終出力移動に失敗した場合**、文字起こし結果保護のため work dir を残し、UI にパスを提示する(「終了時掃除」仕様の例外)で良いか — 推奨: はい(数時間の処理結果を消さない)
4. **preset vad=true で silero モデル未取得**の場合: ジョブ開始を拒否し M3-2 の DL 誘導に委ねる(黙って VAD なし実行はしない)で良いか — 推奨: はい(whisper-cli 自体が VAD 未対応の場合のみ VAD なしフォールバック=仕様どおり)
5. 状態機械は 5 状態 + `phase`(converting/transcribing/moving)で良いか — 推奨: はい

推奨: 統合案で承認いただければ、M1-3 → M1-4 の順に実装する(M1-4 は G4 対象)。

## D. 人間承認結果(2026-07-10)

スタブ契約に対する設計・実装を以下の条件で承認。M0-3 の実機結果を反映するまで M1-4 を最終完了扱いにしない。

1. job が pipeline orchestration、exec.Engine が CLI 引数・実行・path 単位の VAD feature detection を所有する。model/VAD path は server/main 側で解決して RunRequest に渡す
2. 5 状態を維持し、running 中の `phase=converting|transcribing|moving` で工程を表す
3. multipart は検証済み preset と単一 `file` part のみ。ユーザー filename を保存 path に使わず、upload 完了後に 202 を返す
4. body 全体 8 GiB 上限。queued job は最大 4 件
5. Downloads 不在時は `~/whisper-cpp-gui/`。publish 失敗時だけ recovery marker 付き work dir を 7 日保持する
6. `/api/download` は Job に記録した final output directory と output filename allowlist から配信する
7. preset が VAD を要求し CLI が対応する場合だけ VAD model を必須とする。CLI 非対応時だけ VAD なしで実行する
