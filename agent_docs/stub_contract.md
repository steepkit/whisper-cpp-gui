# stub_contract.md — testdata/stubs/ の入出力契約

`testdata/stubs/whisper-cli` / `testdata/stubs/ffmpeg` の挙動契約。実機情報(M0-3 / M5、`docs/notes.md`)が判明したらこの契約とスタブ本体を実フォーマットへ追従させる。

## 共通

- bash スクリプト。実行権限付きで配置する
- 受け取った全引数を `invocation.json` に記録する: `{"stub":"whisper-cli"|"ffmpeg","argv":[...],"env":{...}}`
- `STUB_FAIL=1`: invocation.json 記録**後**に stderr へ 1 行出して非ゼロ終了(掃除テスト用に「呼ばれた事実」は残る)
- `STUB_IGNORE_TERM=1`: 通常実行時に SIGTERM を無視し、process group への SIGKILL fallback を再現する。`--help` には影響しない

## whisper-cli

| 入力 | 挙動 |
|---|---|
| `--help` / `-h` | help を stdout に出して即終了(invocation.json は書かない)。既定で `--vad` / `--vad-model` を含む |
| `STUB_NO_VAD=1` + `--help` | help から VAD フラグが消える |
| `STUB_NO_VAD=1` + `--vad` / `--vad-model` | `error: unknown argument: --vad` を stderr に出して exit 1 |
| `-of PREFIX` / `--output-file PREFIX` | `dirname(PREFIX)` に invocation.json を書き、完了時に `PREFIX.txt/.srt/.vtt` を生成 |
| `-otxt` / `-osrt` / `-ovtt` | 指定された形式のみ生成。どれも無ければ 3 形式すべて生成 |
| `-of` なし | invocation.json はカレントディレクトリ、出力ファイルは生成しない |
| `-pp` / `--print-progress` | stderr に `whisper_print_progress_callback: progress = 25%`(25/50/75/100)を 1 行ずつ、各行前に interval 待機。未指定なら進捗行なし |
| `STUB_PROGRESS_INTERVAL` | interval を上書き。`10ms` / `0.5s` / `2`(秒)を受理。既定 1 秒 |
| `STUB_IGNORE_TERM=1` | SIGTERM を無視したまま進捗待機を続ける。M1-3 の grace 経過後 SIGKILL テスト用 |

出力の固定内容: txt は 2 行のダミー文、srt/vtt は同内容の 2 セグメント。

## ffmpeg

| 入力 | 挙動 |
|---|---|
| 最終引数 | 出力ファイルパスとして扱う(ffmpeg 慣例)。ディレクトリを作成し、そこに invocation.json も書く |
| 成功時 | 出力パスへ最小の正当な WAV(16kHz mono s16le, 3200 バイトデータ)を書き、stderr に進捗風 1 行 |
| 引数 0 個 | exit 1 |

## 実機契約

2026-07-10 に Linuxbrew `whisper-cpp 1.9.1` で確認済み。詳細は `whisper_cli_reference.md`。macOS、実 large-v3 モデル、長時間音声は `docs/notes.md` M5 に残す。
