// Package config provides embedded application configuration.
package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
)

const (
	outputTXT = "txt"
	outputSRT = "srt"
	outputVTT = "vtt"
)

var allowedOutputs = map[string]struct{}{
	outputTXT: {},
	outputSRT: {},
	outputVTT: {},
}

//go:embed presets.json
var embeddedPresets []byte

// Preset is one supported transcription configuration. Name is an i18n key.
type Preset struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Model   string   `json:"model"`
	VAD     bool     `json:"vad"`
	Outputs []string `json:"outputs"`
}

type presetJSON struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Model   string   `json:"model"`
	VAD     *bool    `json:"vad"`
	Outputs []string `json:"outputs"`
}

// Catalog is the validated set of presets bundled with the application.
type Catalog struct {
	presets []Preset
	byID    map[string]Preset
}

// Load returns the validated preset catalog embedded in the application.
func Load() (*Catalog, error) {
	return load(embeddedPresets)
}

// List returns presets in their bundled order. The result and every Outputs
// slice are copies and may be modified by the caller.
func (c *Catalog) List() []Preset {
	if c == nil {
		return nil
	}
	return copyPresets(c.presets)
}

// Lookup returns a copy of the preset identified by id.
func (c *Catalog) Lookup(id string) (Preset, bool) {
	if c == nil {
		return Preset{}, false
	}
	preset, ok := c.byID[id]
	if !ok {
		return Preset{}, false
	}
	return copyPreset(preset), true
}

func load(data []byte) (*Catalog, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var decoded []presetJSON
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode presets: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("validate presets: no presets configured")
	}

	presets := make([]Preset, 0, len(decoded))
	byID := make(map[string]Preset, len(decoded))
	for index, item := range decoded {
		if item.VAD == nil {
			return nil, fmt.Errorf("validate preset %d: vad must be present", index)
		}
		preset := Preset{
			ID:      item.ID,
			Name:    item.Name,
			Model:   item.Model,
			VAD:     *item.VAD,
			Outputs: item.Outputs,
		}
		if err := validatePreset(preset); err != nil {
			return nil, fmt.Errorf("validate preset %d: %w", index, err)
		}
		if _, exists := byID[preset.ID]; exists {
			return nil, fmt.Errorf("validate preset %d: duplicate id %q", index, preset.ID)
		}
		byID[preset.ID] = copyPreset(preset)
		presets = append(presets, preset)
	}

	return &Catalog{presets: copyPresets(presets), byID: byID}, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode presets: unexpected trailing JSON value")
		}
		return fmt.Errorf("decode presets: trailing data: %w", err)
	}
	return nil
}

func validatePreset(preset Preset) error {
	if !safeID(preset.ID) {
		return fmt.Errorf("id %q is not safe", preset.ID)
	}
	if preset.Name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if preset.Model == "" {
		return fmt.Errorf("model must not be empty")
	}
	if len(preset.Outputs) == 0 {
		return fmt.Errorf("outputs must not be empty")
	}

	outputs := make(map[string]struct{}, len(preset.Outputs))
	for _, output := range preset.Outputs {
		if _, allowed := allowedOutputs[output]; !allowed {
			return fmt.Errorf("output %q is not allowed", output)
		}
		if _, duplicate := outputs[output]; duplicate {
			return fmt.Errorf("duplicate output %q", output)
		}
		outputs[output] = struct{}{}
	}
	return nil
}

func safeID(id string) bool {
	if id == "" {
		return false
	}
	for index, char := range id {
		if char >= 'a' && char <= 'z' {
			continue
		}
		if index > 0 && ((char >= '0' && char <= '9') || char == '_') {
			continue
		}
		return false
	}
	return true
}

func copyPreset(preset Preset) Preset {
	preset.Outputs = append([]string(nil), preset.Outputs...)
	return preset
}

func copyPresets(presets []Preset) []Preset {
	copy := make([]Preset, len(presets))
	for index, preset := range presets {
		copy[index] = copyPreset(preset)
	}
	return copy
}
