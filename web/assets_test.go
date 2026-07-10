package web

import (
	"encoding/json"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

var (
	i18nAttribute = regexp.MustCompile(`data-i18n(?:-[a-z-]+)?="([^"]+)"`)
	uiKeyLiteral  = regexp.MustCompile(`"((?:screen|preset)\.[^"]+)"`)
	htmlTextNode  = regexp.MustCompile(`>\s*([^<>\s][^<>]*)<`)
	htmlTextAttr  = regexp.MustCompile(`\b(title|alt|placeholder|aria-label)\s*=\s*("[^"]+"|'[^']+')`)
	jsTextLiteral = regexp.MustCompile(`(?:textContent|innerText)\s*=\s*(?:"[^"]+"|'[^']+'|` + "`[^`]+`" + `)`)
)

func TestBootstrapRemovesTokenFragmentBeforeAppStartup(t *testing.T) {
	index, err := fs.ReadFile(Assets, "index.html")
	if err != nil {
		t.Fatalf("reading index.html: %v", err)
	}
	indexContent := string(index)
	for _, required := range []string{`src="/bootstrap.js"`, `src="/app.js"`, `href="/style.css"`} {
		if !strings.Contains(indexContent, required) {
			t.Errorf("index.html is missing %q", required)
		}
	}
	if strings.Index(indexContent, `src="/bootstrap.js"`) > strings.Index(indexContent, `src="/app.js"`) {
		t.Fatal("bootstrap.js must load before app.js")
	}

	script, err := fs.ReadFile(Assets, "bootstrap.js")
	if err != nil {
		t.Fatalf("reading bootstrap.js: %v", err)
	}
	content := string(script)
	for _, required := range []string{"window.location.hash", "window.history.replaceState", "__WHISPER_CPP_GUI__"} {
		if !strings.Contains(content, required) {
			t.Errorf("bootstrap.js is missing %q", required)
		}
	}
	if strings.Index(content, "window.history.replaceState") > strings.Index(content, "Object.defineProperty") {
		t.Error("bootstrap.js must remove the fragment before exposing bootstrap state")
	}
}

func TestAssetsEmbedEveryBrowserResource(t *testing.T) {
	for _, path := range []string{
		"index.html",
		"bootstrap.js",
		"app.js",
		"style.css",
		"locales/ja.json",
	} {
		if _, err := fs.ReadFile(Assets, path); err != nil {
			t.Errorf("embedded asset %q is unavailable: %v", path, err)
		}
	}
}

func TestLocaleKeysCoverMarkupAndScript(t *testing.T) {
	locale := readLocale(t)
	for _, path := range []string{"index.html", "app.js"} {
		content := readAsset(t, path)
		for _, match := range i18nAttribute.FindAllStringSubmatch(content, -1) {
			if _, ok := locale[match[1]]; !ok {
				t.Errorf("%s references absent locale key %q", path, match[1])
			}
		}
		for _, match := range uiKeyLiteral.FindAllStringSubmatch(content, -1) {
			if _, ok := locale[match[1]]; !ok {
				t.Errorf("%s references absent locale key %q", path, match[1])
			}
		}
	}

	for _, key := range []string{
		"screen.job.status.queued",
		"screen.job.status.running",
		"screen.job.status.done",
		"screen.job.status.failed",
		"screen.job.status.cancelled",
		"screen.job.phase.converting",
		"screen.job.phase.transcribing",
		"screen.job.phase.moving",
		"screen.error.run_failed",
		"screen.error.timeout",
		"screen.error.manager_closed",
		"screen.error.model_missing",
		"screen.error.vad_model_missing",
		"screen.error.conversion_failed",
		"screen.error.transcription_failed",
		"screen.error.output_publish_failed",
		"screen.models.name.large-v3-q5_0",
		"screen.models.name.large-v3-turbo-q5_0",
		"screen.models.name.silero-vad",
		"screen.models.state.missing",
		"screen.models.state.downloading",
		"screen.models.state.downloaded",
		"screen.models.state.invalid",
		"screen.models.error.network_error",
		"screen.models.error.http_status",
		"screen.models.error.content_length",
		"screen.models.error.size_mismatch",
		"screen.models.error.checksum_mismatch",
		"screen.models.error.storage_error",
		"screen.models.error.redirect_policy",
		"screen.models.error.timeout",
		"screen.models.error.cancelled",
		"screen.models.error.model_in_use",
		"screen.models.error.download_in_progress",
	} {
		if _, ok := locale[key]; !ok {
			t.Errorf("locale is missing dynamic UI key %q", key)
		}
	}
}

func TestNoVisibleStringLiteralsOutsideLocale(t *testing.T) {
	for _, path := range []string{"index.html", "app.js", "style.css"} {
		content := readAsset(t, path)
		for _, r := range content {
			if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
				t.Errorf("%s contains hardcoded CJK UI text", path)
				break
			}
		}
		if path != "index.html" {
			continue
		}
		for _, match := range htmlTextNode.FindAllStringSubmatch(content, -1) {
			t.Errorf("index.html contains raw text node %q", strings.TrimSpace(match[1]))
		}
		for _, match := range htmlTextAttr.FindAllStringSubmatch(content, -1) {
			t.Errorf("index.html contains literal %s attribute %q", match[1], match[2])
		}
	}
	if match := jsTextLiteral.FindString(readAsset(t, "app.js")); match != "" {
		t.Errorf("app.js contains visible text literal assignment %q", match)
	}
}

func TestMainControlsHaveSemanticNames(t *testing.T) {
	index := readAsset(t, "index.html")
	for _, required := range []string{
		`<form id="job-form"`,
		`<input class="visually-hidden" id="file-input" type="file" accept="audio/*,video/*" data-i18n-aria=`,
		`<button class="drop-zone" id="drop-zone" type="button" data-i18n=`,
		`<fieldset class="control-group" id="preset-group">`,
		`<input id="vad-option" type="checkbox">`,
		`<button class="primary-action" id="start-button" type="submit" data-i18n=`,
		`<button class="danger-action" id="cancel-button" type="button" data-i18n=`,
		`<progress id="job-progress"`,
		`<details class="log-panel" id="log-panel">`,
		`role="alert" aria-live="assertive"`,
		`<section class="required-model-notice" id="required-model-notice" hidden`,
		`<button class="secondary-action" id="required-model-action" type="button">`,
		`<section class="model-panel" id="model-panel" aria-labelledby="model-title">`,
		`<button class="model-refresh" id="model-refresh" type="button" data-i18n=`,
		`<ul class="model-list" id="model-list" aria-busy="true">`,
	} {
		if !strings.Contains(index, required) {
			t.Errorf("main UI is missing semantic control marker %q", required)
		}
	}
}

func TestAppUsesM2APIContract(t *testing.T) {
	app := readAsset(t, "app.js")
	for _, required := range []string{
		`window.__WHISPER_CPP_GUI__.token`,
		`"X-Auth-Token"`,
		`"/api/config"`,
		`form.append("preset", preset.id)`,
		`form.append("file", state.selectedFile, state.selectedFile.name)`,
		`form.append("options", JSON.stringify({`,
		`typeof job.progress === "number"`,
		"new EventSource(`/api/jobs/${encodeURIComponent(id)}/events?token=${token}`)",
		`/cancel`,
		`/outputs`,
		"/api/download/${encodeURIComponent(jobID)}/${encodeURIComponent(file)}`",
		`response.blob()`,
		`URL.createObjectURL(blob)`,
		`elements.jobControls.disabled = !usable`,
		`elements.cancelButton.disabled = !jobBusy || state.cancelPending`,
		`Array.from(navigator.languages)`,
		`document.documentElement.lang = name`,
		`elements.recoveryPath.textContent = recoveryPath ? t(K.recoveryPath`,
		`elements.logList.querySelector(".log-empty")`,
	} {
		if !strings.Contains(app, required) {
			t.Errorf("app.js is missing API contract marker %q", required)
		}
	}
}

func TestAppUsesM3ModelContract(t *testing.T) {
	app := readAsset(t, "app.js")
	for _, required := range []string{
		`const sileroVADModel = "silero-vad"`,
		`normalizeModelManifest(value.models)`,
		`normalizeModelsPayload`,
		`normalizeModelsPayload(await response.json(), state.config && state.config.models)`,
		`modelDescriptorsEqual(descriptor, expectedDescriptor)`,
		`requiredModelsDownloaded()`,
		`snapshot.bytes_downloaded === snapshot.model.size`,
		`state.modelActions.has(name)`,
		`state.modelEventSources`,
		`apiFetch("/api/models", { method: "GET" })`,
		"/api/models/${encodeURIComponent(name)}/download`",
		"/api/models/${encodeURIComponent(name)}/events?token=${token}`",
		"apiFetch(`/api/models/${encodeURIComponent(name)}`, { method: \"DELETE\" })",
		`window.confirm(t(K.modelActionDeleteConfirm`,
		`elements.startButton.disabled =`,
		`!modelsReady`,
		`closeAllModelEvents()`,
		`state.modelVersions.get(name) !== version`,
	} {
		if !strings.Contains(app, required) {
			t.Errorf("app.js is missing M3 model contract marker %q", required)
		}
	}
}

func readAsset(t *testing.T, path string) string {
	t.Helper()
	data, err := fs.ReadFile(Assets, path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func readLocale(t *testing.T) map[string]string {
	t.Helper()
	var locale map[string]string
	if err := json.Unmarshal([]byte(readAsset(t, "locales/ja.json")), &locale); err != nil {
		t.Fatalf("parsing ja locale: %v", err)
	}
	return locale
}
