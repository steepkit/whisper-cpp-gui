# whisper-cpp-gui

`whisper-cpp-gui` is a local transcription interface for
[whisper.cpp](https://github.com/ggerganov/whisper.cpp). A single Go binary
starts an HTTP server on `127.0.0.1` and opens the browser as its GUI. Audio,
video, and transcripts stay on the machine; the only application-initiated
external transfer is a model download requested by the user.

The primary target is Apple Silicon macOS. Linux remains supported by keeping
OS-specific discovery and browser launch behavior isolated.

> **v0.1.0 validation notice:** no physical Apple Silicon Mac was available.
> GitHub macOS 14 build/startup and Homebrew source-install CI are required,
> but real media, 8 GB large-v3 memory use, Downloads permissions, automatic
> browser launch, and real model downloads remain unverified. The complete
> physical workflow, including presets, VAD, outputs, progress, cancellation,
> bootstrap recovery, and browser downloads, was not run. See
> [ADR 0013](docs/adr/0013-waive-physical-mac-validation-for-v0.1.0.md).

For the Japanese lab-user guide, see [docs/install_ja.md](docs/install_ja.md).

## Install

The release repositories and tap must be accessible before these commands can
work. During private staging, only authorized maintainers can access them; that
state is not considered an end-user release.

```bash
brew install steepkit/tap/whisper-cpp-gui
whisper-cpp-gui
```

The fully qualified install name limits Homebrew trust to this formula.
Homebrew installs `whisper-cpp` and `ffmpeg` as runtime dependencies and Go as
a build-only dependency. End users do not need to install or manage Go
separately.

At first launch, choose a preset and download the requested Whisper and VAD
models in the browser. Select an audio or video file, choose output formats,
and start transcription. Completed files are published under
`~/Downloads/whisper-cpp-gui/` (or `~/whisper-cpp-gui/` when `~/Downloads`
does not exist).

## Command line

```text
whisper-cpp-gui             Start the server and open the default browser
whisper-cpp-gui --version   Print the build version and exit
whisper-cpp-gui --port N    Use a fixed loopback port (development/CI)
whisper-cpp-gui --no-browser  Do not launch a browser (development/CI)
```

When automatic browser launch fails, open the private file shown after
`Bootstrap file:`. The token-free `Open: http://127.0.0.1:...` URL is not a
replacement for that file because API access requires the startup token.

## Development

Requirements:

- Go 1.22 or newer
- Bash and ShellCheck for shell-script validation
- no real `whisper-cli`, `ffmpeg`, models, or macOS host for the automated test
  suite

The Go module intentionally uses only the standard library. Browser assets are
plain JavaScript and CSS embedded in the binary; there is no npm or bundler
step.

```bash
gofmt_out="$(gofmt -l .)" && test -z "$gofmt_out"
go build ./...
go vet ./...
go test ./...
shellcheck scripts/smoke_start.sh testdata/stubs/*
```

Build and exercise a release-like command:

```bash
mkdir -p dist
go build -trimpath -ldflags="-s -w -X main.version=0.1.0-test" \
  -o dist/whisper-cpp-gui ./cmd/whisper-cpp-gui
scripts/smoke_start.sh ./dist/whisper-cpp-gui 0.1.0-test
```

Tests use the deterministic binaries in `testdata/stubs/`. Record behavior
that requires real Homebrew tools, models, or a Mac in
[docs/notes.md](docs/notes.md), not as a code TODO.

## Architecture

```text
cmd/whisper-cpp-gui/  process wiring and startup only
internal/server/      localhost HTTP, auth, Host/Origin checks, SSE
internal/job/         serial queue, state machine, cancellation, publishing
internal/exec/        whisper-cli and ffmpeg discovery/execution
internal/model/       fixed manifest, verified downloads, leases
web/                  embedded static SPA and Japanese locale
```

The dependency direction is `server -> job -> exec`; the model package is a
leaf used by the server and command wiring.

## Security and privacy

- The server binds only to `127.0.0.1` and validates Host and Origin.
- Each launch creates a random 256-bit token. Ordinary APIs accept it only in
  `X-Auth-Token`.
- A private mode-`0600` bootstrap file transfers the token in a URL fragment;
  the SPA removes the fragment immediately. The validated token and active job
  ID are kept only in same-tab `sessionStorage` so a reload can reconnect to
  the job. They are not written to `localStorage` or cookies. The token is not
  put in launcher arguments or application logs.
- Native `EventSource` cannot set the auth header, so job and model SSE URLs
  carry the token in a query parameter. Those URLs can appear in browser
  diagnostics. This is an accepted residual risk, reduced by loopback-only
  binding, Host/Origin checks, `no-referrer`, no-store responses, and never
  logging the request query.
- Uploads stream to private per-user storage. Child commands are executed with
  argument arrays, never through a shell.
- Model downloads begin only after a user action and use commit-pinned HTTPS
  sources with host, size, and SHA256 verification. Audio, video, transcripts,
  and telemetry are not sent anywhere.

The threat model does not attempt to defend against a malicious process already
running as the same OS user.

## Project documents

- [Implementation plan](docs/implementation_plan.md)
- [Agent workflow and review gates](docs/agentic_workflow.md)
- [Architecture decisions](docs/adr/README.md)
- [Real-machine verification backlog](docs/notes.md)
- [Manual release runbook](docs/releasing.md)

## License

[MIT](LICENSE)
