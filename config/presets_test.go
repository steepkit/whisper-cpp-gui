package config

import (
	"strings"
	"testing"
)

func TestLoadBuiltInPresets(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := []Preset{
		{
			ID:      "ja_fast",
			Name:    "preset.ja_fast.name",
			Model:   "large-v3-turbo-q5_0",
			VAD:     true,
			Outputs: []string{"txt", "srt", "vtt"},
		},
		{
			ID:      "ja_accurate",
			Name:    "preset.ja_accurate.name",
			Model:   "large-v3-q5_0",
			VAD:     true,
			Outputs: []string{"txt", "srt", "vtt"},
		},
	}
	got := catalog.List()
	if len(got) != len(want) {
		t.Fatalf("List length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if !equalPreset(got[index], want[index]) {
			t.Errorf("List()[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}

	preset, ok := catalog.Lookup("ja_fast")
	if !ok || !equalPreset(preset, want[0]) {
		t.Errorf("Lookup(ja_fast) = %#v, %t; want %#v, true", preset, ok, want[0])
	}
	if _, ok := catalog.Lookup("missing"); ok {
		t.Error("Lookup(missing) returned a preset")
	}
}

func TestCatalogReturnsDefensiveCopies(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	listed := catalog.List()
	listed[0].ID = "changed"
	listed[0].Outputs[0] = "changed"
	lookedUp, ok := catalog.Lookup("ja_fast")
	if !ok {
		t.Fatal("Lookup(ja_fast) did not find the preset")
	}
	if lookedUp.ID != "ja_fast" || lookedUp.Outputs[0] != "txt" {
		t.Errorf("catalog mutated through List: %#v", lookedUp)
	}

	lookedUp.Outputs[0] = "changed"
	again, ok := catalog.Lookup("ja_fast")
	if !ok || again.Outputs[0] != "txt" {
		t.Errorf("catalog mutated through Lookup: %#v", again)
	}
}

func TestLoadRejectsInvalidPresets(t *testing.T) {
	valid := `[{"id":"valid_1","name":"preset.valid.name","model":"model","vad":true,"outputs":["txt"]}]`
	cases := []struct {
		name string
		json string
	}{
		{name: "empty list", json: `[]`},
		{name: "unknown field", json: `[{"id":"valid","name":"name","model":"model","vad":false,"outputs":["txt"],"extra":true}]`},
		{name: "trailing value", json: valid + ` {}`},
		{name: "empty id", json: `[{"id":"","name":"name","model":"model","vad":false,"outputs":["txt"]}]`},
		{name: "path-like id", json: `[{"id":"../valid","name":"name","model":"model","vad":false,"outputs":["txt"]}]`},
		{name: "uppercase id", json: `[{"id":"Valid","name":"name","model":"model","vad":false,"outputs":["txt"]}]`},
		{name: "empty name", json: `[{"id":"valid","name":"","model":"model","vad":false,"outputs":["txt"]}]`},
		{name: "empty model", json: `[{"id":"valid","name":"name","model":"","vad":false,"outputs":["txt"]}]`},
		{name: "missing vad", json: `[{"id":"valid","name":"name","model":"model","outputs":["txt"]}]`},
		{name: "empty outputs", json: `[{"id":"valid","name":"name","model":"model","vad":false,"outputs":[]}]`},
		{name: "unknown output", json: `[{"id":"valid","name":"name","model":"model","vad":false,"outputs":["json"]}]`},
		{name: "duplicate output", json: `[{"id":"valid","name":"name","model":"model","vad":false,"outputs":["txt","txt"]}]`},
		{name: "duplicate id", json: valid[:len(valid)-1] + `,{"id":"valid_1","name":"other","model":"other","vad":false,"outputs":["srt"]}]`},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := load([]byte(test.json))
			if err == nil {
				t.Fatal("load succeeded, want error")
			}
		})
	}
}

func TestLoadAcceptsTrailingWhitespace(t *testing.T) {
	data := "[{\"id\":\"valid\",\"name\":\"name\",\"model\":\"model\",\"vad\":false,\"outputs\":[\"txt\"]}] \n\t"
	if _, err := load([]byte(data)); err != nil {
		t.Fatalf("load with whitespace: %v", err)
	}
}

func equalPreset(got, want Preset) bool {
	return got.ID == want.ID &&
		got.Name == want.Name &&
		got.Model == want.Model &&
		got.VAD == want.VAD &&
		strings.Join(got.Outputs, ",") == strings.Join(want.Outputs, ",")
}
