package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
)

func TestProgressWriterCoalescesAndRecordsWriteFailures(t *testing.T) {
	var notifications []int64
	writer := &progressWriter{
		writer:   io.Discard,
		expected: 10,
		step:     4,
		onWrite:  func(total int64) { notifications = append(notifications, total) },
	}
	for range 10 {
		if _, err := writer.Write([]byte{'x'}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	want := []int64{4, 8, 10}
	if len(notifications) != len(want) {
		t.Fatalf("notifications = %v, want %v", notifications, want)
	}
	for index := range want {
		if notifications[index] != want[index] {
			t.Fatalf("notifications = %v, want %v", notifications, want)
		}
	}

	writeFailure := errors.New("disk full")
	failing := &progressWriter{
		writer:   errorWriter{err: writeFailure},
		expected: 1,
		step:     1,
		onWrite:  func(int64) {},
	}
	if _, err := failing.Write([]byte{'x'}); !errors.Is(err, writeFailure) {
		t.Fatalf("failing Write error = %v", err)
	}
	if code := classifyCopyError(context.Background(), false, failing.writeErr); code != ErrorCodeStorage {
		t.Fatalf("write failure code = %q, want %q", code, ErrorCodeStorage)
	}
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestValidateDownloadURLPolicy(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		allowed bool
	}{
		{name: "origin", rawURL: "https://huggingface.co/path", allowed: true},
		{name: "lfs", rawURL: "https://cdn-lfs.huggingface.co/path?signature=secret", allowed: true},
		{name: "cdn suffix", rawURL: "https://region.cdn.hf.co/path", allowed: true},
		{name: "xet suffix", rawURL: "https://cas-bridge.xethub.hf.co/path", allowed: true},
		{name: "downgrade", rawURL: "http://huggingface.co/path", allowed: false},
		{name: "userinfo", rawURL: "https://user@huggingface.co/path", allowed: false},
		{name: "custom port", rawURL: "https://huggingface.co:443/path", allowed: false},
		{name: "wrong host", rawURL: "https://example.com/path", allowed: false},
		{name: "suffix confusion", rawURL: "https://evilcdn.hf.co/path", allowed: false},
		{name: "suffix parent", rawURL: "https://cdn.hf.co/path", allowed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := url.Parse(test.rawURL)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			err = validateDownloadURL(parsed)
			if (err == nil) != test.allowed {
				t.Errorf("validateDownloadURL(%q) error = %v, allowed=%v", test.rawURL, err, test.allowed)
			}
		})
	}
}

func TestDefaultDownloadClientDoesNotUseEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	client := downloadClient(nil)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default download transport has a proxy callback")
	}
}
