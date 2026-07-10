package job

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxOutputStemBytes = 96

var allowedOutputFormats = map[string]struct{}{
	"txt": {},
	"srt": {},
	"vtt": {},
}

type PublishRequest struct {
	JobID            string
	WorkDir          string
	OutputPrefix     string
	OriginalFilename string
	Formats          []string
}

type PublisherConfig struct {
	HomeDir  func() (string, error)
	CopyFile func(context.Context, string, string) error
}

type Publisher struct {
	homeDir  func() (string, error)
	copyFile func(context.Context, string, string) error
}

func NewPublisher(config PublisherConfig) *Publisher {
	homeDir := config.HomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	copyFile := config.CopyFile
	if copyFile == nil {
		copyFile = copyRegularFile
	}
	return &Publisher{homeDir: homeDir, copyFile: copyFile}
}

func (p *Publisher) Publish(ctx context.Context, request PublishRequest) (RunResult, error) {
	if p == nil || p.homeDir == nil || p.copyFile == nil {
		return RunResult{}, fmt.Errorf("publisher is not configured")
	}
	if !validJobID(request.JobID) {
		return RunResult{}, fmt.Errorf("invalid publish job ID")
	}
	formats, err := validateOutputFormats(request.Formats)
	if err != nil {
		return RunResult{}, err
	}
	workDir, err := filepath.Abs(request.WorkDir)
	if err != nil {
		return RunResult{}, fmt.Errorf("resolve publish work directory: %w", err)
	}
	if err := requireRealDirectory(workDir); err != nil {
		return RunResult{}, fmt.Errorf("validate publish work directory: %w", err)
	}
	prefix, err := filepath.Abs(request.OutputPrefix)
	if err != nil {
		return RunResult{}, fmt.Errorf("resolve output prefix: %w", err)
	}
	if err := requirePathWithin(workDir, prefix); err != nil {
		return RunResult{}, err
	}
	if err := requireRealDirectory(filepath.Dir(prefix)); err != nil {
		return RunResult{}, fmt.Errorf("validate generated output directory: %w", err)
	}

	root, err := p.outputRoot()
	if err != nil {
		return RunResult{}, err
	}
	stem := sanitizeOutputStem(request.OriginalFilename)
	finalDir, reservation, err := reserveOutputName(root, stem)
	if err != nil {
		return RunResult{}, err
	}
	reservationOwned := true
	defer func() {
		if reservationOwned {
			_ = os.Remove(reservation)
		}
	}()

	staging := filepath.Join(root, "."+stem+"."+request.JobID+".partial")
	if err := os.Mkdir(staging, 0o700); err != nil {
		return RunResult{}, fmt.Errorf("create output staging directory: %w", err)
	}
	stagingOwned := true
	defer func() {
		if stagingOwned {
			_ = os.RemoveAll(staging)
		}
	}()

	filenames := make([]string, 0, len(formats))
	for _, format := range formats {
		if err := ctx.Err(); err != nil {
			return RunResult{}, err
		}
		source := prefix + "." + format
		if err := requireRegularFile(source); err != nil {
			return RunResult{}, fmt.Errorf("validate generated %s output: %w", format, err)
		}
		filename := "transcript." + format
		if err := p.copyFile(ctx, source, filepath.Join(staging, filename)); err != nil {
			return RunResult{}, fmt.Errorf("copy %s output: %w", format, err)
		}
		filenames = append(filenames, filename)
	}

	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	if _, err := os.Lstat(finalDir); err == nil {
		return RunResult{}, fmt.Errorf("reserved output destination became occupied")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return RunResult{}, fmt.Errorf("recheck reserved output destination: %w", err)
	}
	if err := os.Rename(staging, finalDir); err != nil {
		return RunResult{}, fmt.Errorf("publish output directory: %w", err)
	}
	stagingOwned = false
	if err := os.Remove(reservation); err == nil || errors.Is(err, fs.ErrNotExist) {
		reservationOwned = false
	}
	return RunResult{FinalOutputDir: finalDir, OutputFiles: filenames}, nil
}

func (p *Publisher) outputRoot() (string, error) {
	home, err := p.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("resolve absolute home directory: %w", err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("resolve home directory symlinks: %w", err)
	}
	if err := requireRealDirectory(home); err != nil {
		return "", fmt.Errorf("validate home directory: %w", err)
	}

	downloads := filepath.Join(home, "Downloads")
	base := home
	info, err := os.Lstat(downloads)
	switch {
	case err == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("Downloads exists but is not a real directory")
		}
		base = downloads
	case errors.Is(err, fs.ErrNotExist):
	default:
		return "", fmt.Errorf("inspect Downloads directory: %w", err)
	}

	root := filepath.Join(base, "whisper-cpp-gui")
	if err := ensureRealDirectory(root, 0o700); err != nil {
		return "", fmt.Errorf("prepare output root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", fmt.Errorf("restrict output root permissions: %w", err)
	}
	return root, nil
}

func reserveOutputName(root, stem string) (finalDir, reservation string, err error) {
	for suffix := 1; suffix <= 10_000; suffix++ {
		name := stem
		if suffix > 1 {
			name = fmt.Sprintf("%s-%d", stem, suffix)
		}
		candidate := filepath.Join(root, name)
		if _, err := os.Lstat(candidate); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("inspect output destination: %w", err)
		}
		reservationPath := filepath.Join(root, "."+name+".reserve")
		file, err := os.OpenFile(reservationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", "", fmt.Errorf("reserve output destination: %w", err)
		}
		if closeErr := file.Close(); closeErr != nil {
			_ = os.Remove(reservationPath)
			return "", "", fmt.Errorf("close output reservation: %w", closeErr)
		}
		if _, err := os.Lstat(candidate); err == nil {
			_ = os.Remove(reservationPath)
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			_ = os.Remove(reservationPath)
			return "", "", fmt.Errorf("recheck output destination: %w", err)
		}
		return candidate, reservationPath, nil
	}
	return "", "", fmt.Errorf("could not reserve an output directory name")
}

func validateOutputFormats(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one output format is required")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := allowedOutputFormats[value]; !ok {
			return nil, fmt.Errorf("unsupported output format %q", value)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("duplicate output format %q", value)
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func sanitizeOutputStem(filename string) string {
	filename = strings.ReplaceAll(filename, "\\", "/")
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	var builder strings.Builder
	separator := false
	for _, value := range base {
		allowed := unicode.IsLetter(value) || unicode.IsNumber(value) || value == ' ' || value == '_' || value == '-'
		if allowed {
			builder.WriteRune(value)
			separator = false
			continue
		}
		if builder.Len() > 0 && !separator {
			builder.WriteByte('-')
			separator = true
		}
	}
	stem := strings.Trim(builder.String(), " .-")
	if stem == "" {
		stem = "transcription"
	}
	return utf8PrefixBytes(stem, maxOutputStemBytes)
}

func utf8PrefixBytes(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return strings.Trim(value[:end], " .-")
}

func ensureRealDirectory(path string, mode fs.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a real directory", path)
	}
	return nil
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a real directory", path)
	}
	return nil
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}

func requirePathWithin(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("output prefix must be below the work directory")
	}
	return nil
}

func copyRegularFile(ctx context.Context, source, destination string) (err error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := output.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			if _, err := output.Write(buffer[:count]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := output.Sync(); err != nil {
		return err
	}
	return nil
}
