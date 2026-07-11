# whisper-cpp-gui 導入・利用手順

研究室の Mac で音声・動画をローカル文字起こしするための手順です。音声、動画、文字起こし結果は外部へ送信しません。アプリ起動後にアプリ自身が開始する外部通信は、利用者が画面で開始したモデル取得だけです。Homebrew による導入・更新は別途インターネットへ接続します。

> この手順は public release の配布開始後に使用します。インストール時に repository が見つからない、または 404 になる場合はまだ配布前です。token を URL やコマンドへ追加せず、管理者へ連絡してください。

> **v0.1.1 の未検証事項:** 物理 Apple Silicon Mac を利用できないため、実際の Mac では一度も動作確認していません。実講義録音、「高速」「高精度」の両設定、VAD、txt/srt/vtt 出力、進捗表示、キャンセル、ブラウザからの保存、8GB 機、自動ブラウザ起動、実モデル取得を含む一連の操作が未検証です。
>
> 公開前に GitHub 上の macOS 環境で build、起動、Homebrew install を確認しますが、実際の Mac 操作の代わりにはなりません。高精度モデル利用時のメモリ不足、結果を保存できない、モデルを取得できないなどの問題が残る可能性があります。問題が起きた場合は処理を中止し、入力した音声・動画、文字起こし結果、モデルファイル、token、個人用 path を添付せず repository の Issue へ報告してください。

## 1. Mac と Homebrew を準備する

対象は Apple Silicon (`arm64`) と macOS Sonoma 14 以降です。ソフトウェア導入を組織で制限している Mac、管理者権限がない Mac、通信先を制限しているネットワークでは、先に管理者へ依頼してください。

モデル、入力ファイルの cache copy、16 kHz WAV、完成物が一時的に共存します。長い講義録音では元ファイルの数倍の空きが必要です。最大 upload は 8 GiB なので、大きなファイルでは十分な空き容量を管理者と確認してください。

1. `Command` + `Space` を押し、「ターミナル」と入力して Terminal.app を開きます。
2. 次を実行し、`arm64` と macOS version を確認します。

   ```bash
   uname -m
   sw_vers -productVersion
   ```

   1 行目が `arm64` でない場合、または 2 行目の先頭の数値が `14` 未満の場合は作業を中止し、管理者へ連絡してください。

3. Xcode Command Line Tools を確認します。

   ```bash
   xcode-select -p
   ```

   path が表示されずエラーになった場合だけ、次を実行して画面の案内に従います。

   ```bash
   xcode-select --install
   ```

4. `brew --version` が成功する場合は次の節へ進みます。command not found の場合は、Homebrew の [公式 Installation 手順](https://docs.brew.sh/Installation) にある install command を実行します。

   ```bash
   /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
   ```

   インストールには時間がかかる場合があります。password を求められたときは Mac のログイン password を入力します。入力中は画面に文字や記号が表示されません。

5. installer の最後に表示される `Next steps` を実行します。Apple Silicon の標準構成では次の 2 行です。

   ```bash
   echo 'eval "$(/opt/homebrew/bin/brew shellenv)"' >> ~/.zprofile
   eval "$(/opt/homebrew/bin/brew shellenv)"
   ```

6. Terminal を閉じて開き直し、確認します。

   ```bash
   brew --version
   ```

## 2. whisper-cpp-gui をインストールする

```bash
brew update
brew install steepkit/tap/whisper-cpp-gui
whisper-cpp-gui --version
```

完全修飾名 `steepkit/tap/whisper-cpp-gui` は、Homebrew 6 以降で信頼対象をこの Formula だけに限定します。tap 全体を信頼する必要はありません。詳細は Homebrew の [Tap Trust](https://docs.brew.sh/Tap-Trust) を参照してください。

Go は利用者が管理する実行時依存ではありません。Homebrew がソースビルド時だけ導入します。`whisper-cli` と `ffmpeg` も Formula の依存として導入されます。

## 3. 初回起動

```bash
whisper-cpp-gui
```

Terminal を開いたままにしてください。アプリは `127.0.0.1` のランダムな port で起動し、既定ブラウザを開きます。

ブラウザが開かない場合は、Terminal に表示された `Bootstrap file:` のファイルを Finder または `open` コマンドで開きます。`Open: http://127.0.0.1:...` の URL を直接開いても起動 token がないため操作できません。

同じ tab で画面を再読み込みした場合は、そのまま利用を継続できます。実行中または直前の job がある場合は状態を再取得し、進捗表示・キャンセル・出力取得へ復帰します。新しい tab、履歴からの開き直し、tab を閉じた後、またはアプリ自体を再起動した後に認証エラーになった場合は、Terminal でアプリを起動し直し、新しい bootstrap から開いてください。

## 4. モデルを取得する

初回は文字起こし開始前に必要なモデルを取得します。

1. 画面上部の「高速」または「高精度」を選びます。
2. 「必要なモデルをダウンロード」を押します。
3. 画面下部のモデル管理で、必要なモデルが「取得済み」になるまで待ちます。開始ボタンは、モデル取得に加えてファイル選択と出力形式の選択後に利用可能になります。

| プリセット | Whisper モデル | manifest 上のサイズ | 用途 |
|---|---:|---:|---|
| 高速 | `large-v3-turbo-q5_0` | 574,041,195 bytes | 通常の講義録音 |
| 高精度 | `large-v3-q5_0` | 1,081,140,203 bytes | 精度を優先する場合 |

「無音区間をスキップ」(VAD) が有効な場合は Silero VAD モデル (885,098 bytes) も必要です。保存済みモデルは画面下部のモデル管理から確認・削除できます。削除中または取得中のモデルを使って文字起こしは開始できません。

モデルは通常 `~/Library/Application Support/whisper-cpp-gui/models/` に保存されます。

## 5. 文字起こしする

1. 「ファイルを選択」またはドラッグ&ドロップで音声・動画を選びます。
2. 「高速」または「高精度」を選びます。
3. 必要に応じて「無音区間をスキップ」と「テキスト (.txt)」「字幕 (.srt)」「WebVTT (.vtt)」を選びます。
4. 「文字起こしを開始」を押します。
5. 状態が「完了」になるまで待ちます。実行中は「キャンセル」で中止できます。
6. 完了後、画面の「ダウンロード」にある各ファイルのボタンでブラウザから取得できます。

ジョブは直列に処理され、1 ジョブの上限時間は 12 時間です。キャンセル、通常の失敗、正常完了では一時作業領域を削除します。

完成物は画面のボタンを押す前に、次の場所へ保存済みです。

```text
~/Downloads/whisper-cpp-gui/<整形済み元ファイル名>/transcript.txt
~/Downloads/whisper-cpp-gui/<整形済み元ファイル名>/transcript.srt
~/Downloads/whisper-cpp-gui/<整形済み元ファイル名>/transcript.vtt
```

同名フォルダがある場合は `-2`, `-3` のような番号が付きます。`~/Downloads` が存在しない環境では `~/whisper-cpp-gui/` を使います。`Downloads` が symlink または通常のディレクトリでない場合は、安全のためエラーにします。

出力の公開だけに失敗した場合、画面に recovery path を表示します。Finder の「移動」>「フォルダへ移動」を選び、表示された path を貼り付けて開いてください。生成済みファイルはその中の `transcribe/out.txt`, `out.srt`, `out.vtt` などです。必要なファイルを先にコピーしてください。この作業領域は 7 日を過ぎた後の次回アプリ起動時に削除されます。

## 6. 終了・更新・アンインストール

終了は、起動した Terminal で `Ctrl-C` を押します。ブラウザの tab を閉じるだけではサーバーは終了しません。

更新:

```bash
brew update
brew upgrade steepkit/tap/whisper-cpp-gui
```

アンインストール:

```bash
brew uninstall steepkit/tap/whisper-cpp-gui
brew untrust --formula steepkit/tap/whisper-cpp-gui
brew untap steepkit/tap
```

アンインストールしてもモデル、設定、cache、完成物は自動削除しません。完全に削除する場合は Finder の「移動」>「フォルダへ移動」で下表の設定・モデル・一時ジョブを確認して削除します。完成物は必要性を確認して別に削除してください。Homebrew が `whisper-cpp`, `ffmpeg`, Go を残す場合がありますが、他のソフトウェアも利用する可能性があるため、管理者の確認なしに削除しないでください。

## 保存場所

| データ | macOS の通常の場所 |
|---|---|
| 任意設定 | `~/Library/Application Support/whisper-cpp-gui/config.json` |
| モデル | `~/Library/Application Support/whisper-cpp-gui/models/` |
| 一時ジョブ | `~/Library/Caches/whisper-cpp-gui/jobs/` |
| 完成物 | `~/Downloads/whisper-cpp-gui/`。Downloads を使えない場合は `~/whisper-cpp-gui/` |

## 困ったとき

### Homebrew またはモデル取得が失敗する

Homebrew の導入・更新には GitHub と Homebrew の配布 infrastructure への HTTPS 接続が必要です。モデル取得では次の HTTPS host だけをアプリが許可します。

- `huggingface.co`
- `cdn-lfs.huggingface.co`
- `*.cdn.hf.co`
- `*.xethub.hf.co`

モデル downloader は環境変数の proxy を利用しません。直接 HTTPS 接続を許可していない研究室ネットワークでは、管理者へ上記 host の許可を依頼してください。URL や SHA256 を手作業で変更しないでください。

### CLI バイナリを見つけられない

通常は Homebrew の `/opt/homebrew/bin` を自動検出します。まず変更を加えずに次を確認します。

```bash
which whisper-cli
which ffmpeg
brew list --versions whisper-cpp ffmpeg
```

いずれかが見つからない、または command がエラーになる場合は管理者へ結果を伝えてください。管理者が再導入を判断した場合だけ、次を実行します。

```bash
brew reinstall whisper-cpp ffmpeg
```

別の場所に導入した場合だけ、次の内容を `config.json` に保存します。

```json
{
  "whisper_cli_path": "/absolute/path/to/whisper-cli",
  "ffmpeg_path": "/absolute/path/to/ffmpeg"
}
```

指定先が実行可能でなければ、アプリは `$PATH` と Homebrew の標準場所を続けて探します。JSON が壊れている場合は設定を修正するかファイルを削除してください。

### 開発・検証用オプション

通常利用では指定しません。

```text
--version     version を表示して終了
--port N      localhost server の port を固定
--no-browser  ブラウザ自動起動を抑止
```

`--no-browser` の場合も、Terminal に表示された private bootstrap file を開いてください。

## Security / Privacy note

- server は `127.0.0.1` だけに bind し、Host と Origin を検証します。
- 起動ごとに 256-bit token を生成します。通常 API は `X-Auth-Token` header だけを受け付けます。
- token は権限 `0700` の一時ディレクトリ内にある権限 `0600` の bootstrap HTML で渡し、ブラウザが URL fragment から取得後すぐに削除します。同じ tab の再読み込み回復に必要な token と active job ID だけを `sessionStorage` に保存し、`localStorage` や Cookie には保存しません。launcher の引数、通常の標準出力、アプリログには token を出しません。
- ブラウザ標準の `EventSource` は任意 header を設定できないため、ジョブとモデルの SSE 接続だけは query parameter に token を含めます。この URL がブラウザの Network 表示や診断情報に現れる残余リスクを受容しています。loopback bind、Host/Origin 検証、`Referrer-Policy: no-referrer`、no-store 応答、query をログに出さないことで露出を抑えます。
- 音声、動画、文字起こし結果、telemetry は外部送信しません。モデル取得は利用者の操作時だけ行い、固定 commit の HTTPS URL、サイズ、SHA256 を検証します。

同じ OS user の権限ですでに動く悪意ある process は脅威モデル外です。共有 Mac では自分の user account で利用してください。

## v0.1.1 で残る実機確認

研究室 Mac の実講義録音、Apple Silicon 8GB での large-v3 memory、`open` の自動起動、実モデル取得速度などは [実機検証メモ](notes.md) に未検証として残しています。owner waiver により v0.1.1 の公開は進めますが、これらを検証済みとは扱いません。判断の詳細は [ADR 0016](adr/0016-waive-physical-mac-validation-for-v0.1.1.md) を参照してください。
