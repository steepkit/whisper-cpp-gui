// Command whisper-cpp-gui starts the local transcription GUI server.
// main only wires packages together; it must contain no business logic.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	appconfig "github.com/steepkit/whisper-cpp-gui/config"
	wexec "github.com/steepkit/whisper-cpp-gui/internal/exec"
	"github.com/steepkit/whisper-cpp-gui/internal/job"
	"github.com/steepkit/whisper-cpp-gui/internal/model"
	"github.com/steepkit/whisper-cpp-gui/internal/server"
)

var version = "0.1.0-dev"

const (
	browserLaunchTimeout     = 10 * time.Second
	browserBootstrapLifetime = 5 * time.Minute
)

type application struct {
	server              *server.Server
	bootstrapDir        string
	bootstrapPath       string
	bootstrapCleanup    sync.Once
	bootstrapCleanupErr error
}

func main() {
	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := run(os.Args[1:], os.Stdout, launchBrowser)
	if err != nil {
		fmt.Fprintln(os.Stderr, "whisper-cpp-gui:", err)
		os.Exit(1)
	}
	if app == nil {
		return // --version
	}
	defer app.Close()
	<-shutdown.Done()
}

// run parses flags, starts the server, and (unless suppressed) opens the
// browser. It returns a nil application for informational invocations like
// --version. openBrowser is injected so tests can inspect its token-free
// target and assert it is not called with --no-browser.
func run(args []string, stdout io.Writer, openBrowser func(target string) error) (*application, error) {
	fs := flag.NewFlagSet("whisper-cpp-gui", flag.ContinueOnError)
	fs.SetOutput(stdout)
	showVersion := fs.Bool("version", false, "print version and exit")
	port := fs.Int("port", 0, "fixed port for the localhost server (development/CI use; 0 = random)")
	noBrowser := fs.Bool("no-browser", false, "do not open the browser automatically (development/CI use)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *showVersion {
		fmt.Fprintf(stdout, "whisper-cpp-gui %s\n", version)
		return nil, nil
	}

	token, err := server.NewToken()
	if err != nil {
		return nil, err
	}
	presets, err := appconfig.Load()
	if err != nil {
		return nil, fmt.Errorf("loading presets: %w", err)
	}
	tempRoot, err := job.PrepareTempRoot("")
	if err != nil {
		return nil, err
	}
	if err := job.CleanupStaleWorkDirs(tempRoot, time.Now(), job.DefaultStaleWorkTTL, job.DefaultRecoveryTTL); err != nil {
		slog.Warn("job cleanup completed with warnings", "error", err)
	}
	engine := wexec.NewCLIEngine(wexec.CLIEngineConfig{})
	pipeline, err := job.NewPipeline(job.PipelineConfig{
		Engine:    engine,
		Publisher: job.NewPublisher(job.PublisherConfig{}),
	})
	if err != nil {
		return nil, err
	}
	manager, err := job.NewManager(pipeline, job.Config{TempRoot: tempRoot})
	if err != nil {
		return nil, err
	}
	modelManager, err := model.NewManager(model.Config{})
	if err != nil {
		manager.Close()
		return nil, fmt.Errorf("initializing model manager: %w", err)
	}
	srv, err := server.New(server.Config{
		Port:              *port,
		Token:             token,
		Logger:            slog.Default(),
		Discover:          discoverTools,
		Jobs:              manager,
		Models:            modelManager,
		Presets:           presets,
		TempRoot:          tempRoot,
		ModelPathResolver: model.LocalPath,
		VADLogicalName:    model.ModelSileroVAD,
	})
	if err != nil {
		modelManager.Close()
		manager.Close()
		return nil, err
	}
	baseURL, err := srv.Start()
	if err != nil {
		_ = srv.Close()
		return nil, err
	}
	bootstrapURL := fmt.Sprintf("%s/#token=%s", baseURL, token)
	bootstrapDir, bootstrapPath, err := createBrowserBootstrap(bootstrapURL)
	if err != nil {
		_ = srv.Close()
		return nil, err
	}
	app := &application{
		server:        srv,
		bootstrapDir:  bootstrapDir,
		bootstrapPath: bootstrapPath,
	}
	fmt.Fprintf(stdout, "whisper-cpp-gui %s\nOpen: %s/\n", version, baseURL)
	if *noBrowser {
		fmt.Fprintf(stdout, "Bootstrap file: %s\n", bootstrapPath)
	} else if err := openBrowser(bootstrapPath); err != nil {
		slog.Warn("could not open browser", "error", err)
		fmt.Fprintf(stdout, "Bootstrap file: %s\n", bootstrapPath)
	} else {
		// Browser launchers hand the file off asynchronously. Keep it briefly,
		// then remove it; Close also removes it on normal shutdown.
		time.AfterFunc(browserBootstrapLifetime, func() {
			_ = app.removeBrowserBootstrap()
		})
	}
	return app, nil
}

func (a *application) Close() error {
	return errors.Join(a.server.Close(), a.removeBrowserBootstrap())
}

func (a *application) removeBrowserBootstrap() error {
	a.bootstrapCleanup.Do(func() {
		a.bootstrapCleanupErr = os.RemoveAll(a.bootstrapDir)
	})
	return a.bootstrapCleanupErr
}

// createBrowserBootstrap keeps the bearer token out of launcher argv and
// stdout. Only the current OS user can traverse the directory or read the
// redirect file; the launcher receives the token-free file path.
func createBrowserBootstrap(bootstrapURL string) (string, string, error) {
	encodedURL, err := json.Marshal(bootstrapURL)
	if err != nil {
		return "", "", fmt.Errorf("encoding browser bootstrap URL: %w", err)
	}
	dir, err := os.MkdirTemp("", "whisper-cpp-gui-bootstrap-")
	if err != nil {
		return "", "", fmt.Errorf("creating browser bootstrap directory: %w", err)
	}
	path := filepath.Join(dir, "index.html")
	content := "<!doctype html><html><head><meta charset=\"utf-8\">" +
		"<meta name=\"referrer\" content=\"no-referrer\"></head>" +
		"<body><script>window.location.replace(" + string(encodedURL) + ");</script></body></html>\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", fmt.Errorf("writing browser bootstrap file: %w", err)
	}
	return dir, path, nil
}

// discoverTools wires binary discovery to the real environment: the
// optional user config file, the process PATH, and the host OS.
func discoverTools() wexec.Discovery {
	var cfg wexec.UserConfig
	if path, err := wexec.UserConfigPath(); err == nil {
		if loaded, err := wexec.LoadUserConfig(path); err == nil {
			cfg = loaded
		} else {
			slog.Warn("ignoring unreadable user config", "error", err)
		}
	}
	return wexec.Discover(cfg, os.Getenv("PATH"), runtime.GOOS)
}

// launchBrowser is the single place allowed to branch on runtime.GOOS for
// opening the default browser (AGENTS.md rule 3). target is a private local
// bootstrap file path and never contains the bearer token.
func launchBrowser(target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), browserLaunchTimeout)
	defer cancel()

	var cmd *osexec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = osexec.CommandContext(ctx, "open", target)
	case "linux":
		cmd = osexec.CommandContext(ctx, "xdg-open", target)
	default:
		return fmt.Errorf("no browser launcher for %s; open the printed bootstrap file manually", runtime.GOOS)
	}
	if err := runLauncher(cmd); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("browser launcher timed out: %w", ctx.Err())
		}
		return err
	}
	return nil
}

func runLauncher(cmd *osexec.Cmd) error {
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("browser launcher failed: %w", err)
	}
	return nil
}
