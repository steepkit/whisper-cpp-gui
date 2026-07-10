package model

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

const (
	ModelLargeV3Q50      = "large-v3-q5_0"
	ModelLargeV3TurboQ50 = "large-v3-turbo-q5_0"
	ModelSileroVAD       = "silero-vad"
)

var (
	ErrInvalidModelName = errors.New("invalid model name")
	ErrUnknownModel     = errors.New("unknown model")
)

// Descriptor is immutable manifest metadata safe to expose through an API.
// Source URLs are deliberately not part of this DTO.
type Descriptor struct {
	Name     string `json:"name"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type manifestEntry struct {
	descriptor Descriptor
	repository string
	commit     string
}

var approvedManifest = [...]manifestEntry{
	{
		descriptor: Descriptor{
			Name:     ModelLargeV3Q50,
			Filename: "ggml-large-v3-q5_0.bin",
			Size:     1081140203,
			SHA256:   "d75795ecff3f83b5faa89d1900604ad8c780abd5739fae406de19f23ecd98ad1",
		},
		repository: "ggerganov/whisper.cpp",
		commit:     "5359861c739e955e79d9a303bcbc70fb988958b1",
	},
	{
		descriptor: Descriptor{
			Name:     ModelLargeV3TurboQ50,
			Filename: "ggml-large-v3-turbo-q5_0.bin",
			Size:     574041195,
			SHA256:   "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2",
		},
		repository: "ggerganov/whisper.cpp",
		commit:     "5359861c739e955e79d9a303bcbc70fb988958b1",
	},
	{
		descriptor: Descriptor{
			Name:     ModelSileroVAD,
			Filename: "ggml-silero-v6.2.0.bin",
			Size:     885098,
			SHA256:   "2aa269b785eeb53a82983a20501ddf7c1d9c48e33ab63a41391ac6c9f7fb6987",
		},
		repository: "ggml-org/whisper-vad",
		commit:     "9ffd54a1e1ee413ddf265af9913beaf518d1639b",
	},
}

// Manifest returns a fresh copy of the fixed, ordered manifest.
func Manifest() []Descriptor {
	result := make([]Descriptor, len(approvedManifest))
	for index, entry := range approvedManifest {
		result[index] = entry.descriptor
	}
	return result
}

// Lookup returns manifest metadata for a logical model name.
func Lookup(name string) (Descriptor, error) {
	entry, err := lookupEntry(name)
	if err != nil {
		return Descriptor{}, err
	}
	return entry.descriptor, nil
}

func lookupEntry(name string) (manifestEntry, error) {
	if pathLike(name) {
		return manifestEntry{}, fmt.Errorf("%w: %q", ErrInvalidModelName, name)
	}
	for _, entry := range approvedManifest {
		if entry.descriptor.Name == name {
			return entry, nil
		}
	}
	return manifestEntry{}, fmt.Errorf("%w: %q", ErrUnknownModel, name)
}

func validateManifest(entries []manifestEntry) error {
	if len(entries) == 0 {
		return fmt.Errorf("model manifest must not be empty")
	}
	names := make(map[string]struct{}, len(entries))
	filenames := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		descriptor := entry.descriptor
		if pathLike(descriptor.Name) {
			return fmt.Errorf("invalid model manifest name %q", descriptor.Name)
		}
		if _, exists := names[descriptor.Name]; exists {
			return fmt.Errorf("duplicate model manifest name %q", descriptor.Name)
		}
		names[descriptor.Name] = struct{}{}
		if descriptor.Filename == "" || filepath.Base(descriptor.Filename) != descriptor.Filename ||
			strings.ContainsAny(descriptor.Filename, "/\\") {
			return fmt.Errorf("invalid model manifest filename %q", descriptor.Filename)
		}
		if _, exists := filenames[descriptor.Filename]; exists {
			return fmt.Errorf("duplicate model manifest filename %q", descriptor.Filename)
		}
		filenames[descriptor.Filename] = struct{}{}
		if descriptor.Size <= 0 {
			return fmt.Errorf("invalid model manifest size for %q", descriptor.Name)
		}
		if len(descriptor.SHA256) != sha256HexLength || strings.ToLower(descriptor.SHA256) != descriptor.SHA256 {
			return fmt.Errorf("invalid model manifest checksum for %q", descriptor.Name)
		}
		if _, err := hex.DecodeString(descriptor.SHA256); err != nil {
			return fmt.Errorf("invalid model manifest checksum for %q: %w", descriptor.Name, err)
		}
		if entry.repository != "ggerganov/whisper.cpp" && entry.repository != "ggml-org/whisper-vad" {
			return fmt.Errorf("unapproved model repository for %q", descriptor.Name)
		}
		if len(entry.commit) != commitHexLength || strings.ToLower(entry.commit) != entry.commit {
			return fmt.Errorf("invalid model commit for %q", descriptor.Name)
		}
		if _, err := hex.DecodeString(entry.commit); err != nil {
			return fmt.Errorf("invalid model commit for %q: %w", descriptor.Name, err)
		}
	}
	return nil
}

const (
	sha256HexLength = 64
	commitHexLength = 40
)

func sourceURL(entry manifestEntry) *url.URL {
	return &url.URL{
		Scheme: "https",
		Host:   "huggingface.co",
		Path:   "/" + entry.repository + "/resolve/" + entry.commit + "/" + entry.descriptor.Filename,
	}
}

func pathLike(name string) bool {
	return name == "" || name == "." || name == ".." ||
		filepath.IsAbs(name) || strings.ContainsAny(name, "/\\")
}
