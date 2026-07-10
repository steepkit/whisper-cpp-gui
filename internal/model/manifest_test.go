package model

import (
	"reflect"
	"strings"
	"testing"
)

func TestApprovedManifest(t *testing.T) {
	want := []Descriptor{
		{Name: ModelLargeV3Q50, Filename: "ggml-large-v3-q5_0.bin", Size: 1081140203, SHA256: "d75795ecff3f83b5faa89d1900604ad8c780abd5739fae406de19f23ecd98ad1"},
		{Name: ModelLargeV3TurboQ50, Filename: "ggml-large-v3-turbo-q5_0.bin", Size: 574041195, SHA256: "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2"},
		{Name: ModelSileroVAD, Filename: "ggml-silero-v6.2.0.bin", Size: 885098, SHA256: "2aa269b785eeb53a82983a20501ddf7c1d9c48e33ab63a41391ac6c9f7fb6987"},
	}
	if got := Manifest(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Manifest = %#v, want %#v", got, want)
	}

	first := Manifest()
	first[0].Name = "mutated"
	if got := Manifest()[0].Name; got != ModelLargeV3Q50 {
		t.Fatalf("Manifest shared mutable storage: first name = %q", got)
	}

	wantURLs := []string{
		"https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-large-v3-q5_0.bin",
		"https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-large-v3-turbo-q5_0.bin",
		"https://huggingface.co/ggml-org/whisper-vad/resolve/9ffd54a1e1ee413ddf265af9913beaf518d1639b/ggml-silero-v6.2.0.bin",
	}
	for index, entry := range approvedManifest {
		if got := sourceURL(entry).String(); got != wantURLs[index] {
			t.Errorf("sourceURL(%s) = %q, want %q", entry.descriptor.Name, got, wantURLs[index])
		}
	}
}

func TestValidateManifestRejectsUnsafeMetadata(t *testing.T) {
	base := approvedManifest[2]
	tests := []struct {
		name   string
		mutate func(*manifestEntry)
	}{
		{name: "path filename", mutate: func(entry *manifestEntry) { entry.descriptor.Filename = "../model.bin" }},
		{name: "bad checksum", mutate: func(entry *manifestEntry) { entry.descriptor.SHA256 = strings.Repeat("z", 64) }},
		{name: "zero size", mutate: func(entry *manifestEntry) { entry.descriptor.Size = 0 }},
		{name: "unapproved repository", mutate: func(entry *manifestEntry) { entry.repository = "example/models" }},
		{name: "short commit", mutate: func(entry *manifestEntry) { entry.commit = "deadbeef" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := base
			test.mutate(&entry)
			if err := validateManifest([]manifestEntry{entry}); err == nil {
				t.Fatal("validateManifest accepted unsafe metadata")
			}
		})
	}
	if err := validateManifest([]manifestEntry{base, base}); err == nil {
		t.Fatal("validateManifest accepted duplicate entries")
	}
}
