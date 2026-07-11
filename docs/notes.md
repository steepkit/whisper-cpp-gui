# docs/notes.md — 実機検証項目

実物の whisper-cli / ffmpeg / macOS でしか確認できない事項をここに蓄積する。コード内に TODO を書く代わりにここへ追記し、M0-3 / M5 で消化する。判明した事実はスタブ(`testdata/stubs/`)へ反映する。

## M0-3(最優先・M1-4 の実機互換性/最終完了ゲート)

2026-07-10、Ubuntu 24.04.4 x86_64 + Linuxbrew で確認。`whisper-cpp 1.9.1`、`whisper-cli 1.9.1`、`ffmpeg 6.1.1`。詳細と再現手順は `agent_docs/whisper_cli_reference.md`。

- [x] brew 版 `whisper-cpp` のコマンド名は `whisper-cli`
- [x] `--vad` / `--vad-model` を help で確認。未対応版向け VAD なしフォールバックも実装済み
- [x] `-pp` / `--print-progress` 指定時だけ stderr に `whisper_print_progress_callback: progress = N%`。`-pp` なしでは進捗行なし
- [x] `-of PREFIX` + `-otxt/-osrt/-ovtt` で `PREFIX.txt/.srt/.vtt` を生成
- [x] `ffmpeg -i INPUT -ar 16000 -ac 1 -y OUTPUT` で `sample_rate=16000`, `channels=1`

Formula 同梱の空テストモデルでは `progress = 272%` が一度出るため、パーサは 100 に clamp する。実際の講義録音・large-v3・macOS 固有挙動は M5 で確認する。

## Browser GUI(Ubuntu)

2026-07-11、Ubuntu 上の Firefox 152 + geckodriver で、stub-backed build を固定 loopback port へ起動して確認した。token 値や token を含む一時ファイルは検証記録に残していない。

- [x] private bootstrap file から同一 tab を開くと ready UI へ到達する
- [x] 同一 tab を reload しても認証を維持し、address bar に fragment が残らない
- [x] 同一 tab で不正な `#token=` fragment へ遷移すると fragment を除去し、保存済みの有効 session を維持する
- [x] model EventSource 接続中に同一 port の application を新 token で再起動すると、SSE error 後の header-authenticated probe が 401 を検出し、保存済み token/job state と stream を破棄して認証 error を表示する
- [x] Firefox、geckodriver、application、local proxy、および token-bearing temporary files を検証後に cleanup する

model SSE の確認では HTTPS transfer を応答しない local proxy へ向け、外部 model transfer を発生させていない。再現する場合は stub-backed build を固定 port で起動し、private bootstrap file を Firefox で開いて ready state を確認後、同一 tab の reload と不正 fragment 遷移を行う。この確認は Linux browser の reload/auth path に限定され、物理 Mac、実 workload、実 model download を扱う M5 の代替ではない。

## M5(実機検証)

**v0.1.0 owner waiver (2026-07-11):** repository owner は物理 Apple Silicon Mac を所有せず、利用機会もないため、以下は未検証のままリリースする。これは合格扱いではない。物理 M5 手順全体(両 preset、VAD、txt/srt/vtt、SSE progress/log、cancel、browser download、bootstrap recovery を含む)も未実施である。判断根拠と影響は [ADR 0013](adr/0013-waive-physical-mac-validation-for-v0.1.0.md) を参照。GitHub macOS 14 の build/startup と Homebrew source-install CI は packaging の代替ゲートに限り、実 workload、8GB memory、権限、`open`、実モデル DL を代替しない。

medium プリセットは実測根拠がないため v0.1.0 では追加せず、「高速」「高精度」の 2 種を維持する。物理 Mac で問題が判明した場合は patch release で対応する。

- [ ] 研究室 Mac で実ファイル(講義録音)の文字起こしが通るか
- [ ] Apple Silicon 8GB マシンでの large-v3 メモリ挙動 → 厳しければ medium プリセット追加を判断
- [ ] `~/Downloads/whisper-cpp-gui/` への出力と権限
- [ ] `open` によるブラウザ自動起動の実挙動
- [ ] HF からのモデル DL 実測(サイズ・速度・SHA256 一致)
