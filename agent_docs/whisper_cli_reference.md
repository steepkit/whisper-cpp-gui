# whisper-cli reference

M0-3 で確認した v1 の子プロセス契約。自動テストは実バイナリではなく、この結果を反映した `testdata/stubs/` を使う。

## Verified environment

- Date: 2026-07-10
- OS: Ubuntu 24.04.4 LTS x86_64
- Homebrew: 6.0.6
- `whisper-cpp`: 1.9.1 (Linuxbrew bottle)
- `ffmpeg`: 6.1.1 (`/usr/bin/ffmpeg`)

Formula 同梱の `share/whisper-cpp/for-tests-ggml-tiny.bin` と `jfk.wav` で再現した。テストモデルは tensor を含まないため文字起こし内容は空になるが、引数受理、進捗形式、出力ファイル名の確認には使える。

## Feature detection

```text
usage: whisper-cli [options] file0 file1 ...
-pp,       --print-progress
--vad
-vm FNAME, --vad-model FNAME
```

VAD は起動時の `whisper-cli --help` で検出する。両 VAD flag が無いバージョンでは VAD 引数を付けずに続行する。

## Transcription invocation

```bash
whisper-cli \
  -m MODEL \
  -f INPUT_WAV \
  -of OUTPUT_PREFIX \
  -l ja \
  -pp \
  -otxt -osrt -ovtt \
  --vad --vad-model VAD_MODEL
```

- `-pp` なしでは progress 行は出ない。
- `-pp` ありでは stderr に `whisper_print_progress_callback: progress = N%` が出る。
- Formula の空テストモデルでは `N=272` を観測した。実装は 0 未満を受けず、100 超を 100 に clamp する。
- `-of PREFIX` と各 output flag により `PREFIX.txt`, `PREFIX.srt`, `PREFIX.vtt` が生成される。

## Conversion invocation

```bash
ffmpeg -i INPUT -ar 16000 -ac 1 -y OUTPUT.wav
```

`ffprobe` で `sample_rate=16000`, `channels=1` を確認した。

## Remaining M5 checks

- macOS Homebrew bottle で同じ契約が成立すること
- large-v3 / large-v3-turbo と日本語の実ファイルで 0〜100 の進捗になること
- VAD モデルを使った実際の無音スキップ
- Apple Silicon 8GB での速度とメモリ
