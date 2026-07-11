(() => {
  "use strict";

  const K = Object.freeze({
    authentication: "screen.error.authentication",
    cancelBusy: "screen.action.cancel.busy",
    cancelIdle: "screen.action.cancel.idle",
    connectionError: "screen.connection.error",
    connectionLoading: "screen.connection.loading",
    connectionReady: "screen.connection.ready",
    connectionReconnecting: "screen.connection.reconnecting",
    configuration: "screen.error.configuration",
    copyError: "screen.setup.copy.error",
    copySuccess: "screen.setup.copy.success",
    downloadAction: "screen.download.action",
    errorGeneric: "screen.error.generic",
    filenameEmpty: "screen.upload.filename.empty",
    filenameSelected: "screen.upload.filename.selected",
    logEmpty: "screen.log.empty",
    modelActionDelete: "screen.models.action.delete",
    modelActionDeleteConfirm: "screen.models.action.delete_confirm",
    modelActionDeleteLabel: "screen.models.action.delete_label",
    modelActionDeleting: "screen.models.action.deleting",
    modelActionDownload: "screen.models.action.download",
    modelActionDownloadLabel: "screen.models.action.download_label",
    modelActionDownloading: "screen.models.action.downloading",
    modelActionRedownload: "screen.models.action.redownload",
    modelActionRedownloadLabel: "screen.models.action.redownload_label",
    modelActionRetry: "screen.models.action.retry",
    modelActionRetryLabel: "screen.models.action.retry_label",
    modelEmpty: "screen.models.empty",
    modelErrorGeneric: "screen.models.error.generic",
    modelFilename: "screen.models.filename",
    modelLoading: "screen.models.summary.loading",
    modelNameSeparator: "screen.models.name_separator",
    modelProgressLabel: "screen.models.progress.label",
    modelProgressValue: "screen.models.progress.value",
    modelRequiredAction: "screen.models.required.action",
    modelRequiredActionBusy: "screen.models.required.action_busy",
    modelRequiredChecking: "screen.models.required.checking",
    modelRequiredCheckingMessage: "screen.models.required.checking_message",
    modelRequiredDownloading: "screen.models.required.downloading",
    modelRequiredDownloadingMessage: "screen.models.required.downloading_message",
    modelRequiredMessage: "screen.models.required.message",
    modelRequiredRefresh: "screen.models.required.refresh",
    modelRequiredTitle: "screen.models.required.title",
    modelRequiredUnavailable: "screen.models.required.unavailable",
    modelRequiredUnavailableMessage: "screen.models.required.unavailable_message",
    modelSize: "screen.models.size",
    modelSizeUnitB: "screen.models.size.unit.b",
    modelSizeUnitGB: "screen.models.size.unit.gb",
    modelSizeUnitKB: "screen.models.size.unit.kb",
    modelSizeUnitMB: "screen.models.size.unit.mb",
    modelSizeValue: "screen.models.size.value",
    modelStateLabel: "screen.models.state.label",
    modelSummaryError: "screen.models.summary.error",
    modelSummaryReady: "screen.models.summary.ready",
    presetEmpty: "screen.preset.empty",
    progressUnknown: "screen.job.progress.unknown",
    progressValue: "screen.job.progress.value",
    recoveryPath: "screen.error.recovery_path",
    requestFailed: "screen.error.request_failed",
    setupCommand: "screen.setup.command.value",
    setupCopy: "screen.setup.copy.action",
    setupMissing: "screen.setup.tool.missing",
    startBusy: "screen.action.start.busy",
    startIdle: "screen.action.start.idle",
    statusIdle: "screen.job.status.idle",
  });

  const toolNameKeys = Object.freeze({
    ffmpeg: "screen.setup.tool.ffmpeg",
    whisper_cli: "screen.setup.tool.whisper_cli",
  });

  const modelStates = new Set(["missing", "downloading", "downloaded", "invalid"]);
  const modelNamePattern = /^[a-z0-9][a-z0-9._-]{0,127}$/;
  const modelErrorCodePattern = /^[a-z][a-z0-9_]{0,63}$/;
  const jobIDPattern = /^job-[0-9a-f]{32}$/;
  const activeJobStorageKey = "whisper-cpp-gui.active-job";
  const restoreAttemptLimit = 3;
  const restoreRetryBaseDelay = 250;
  const sileroVADModel = "silero-vad";

  const state = {
    activeJob: null,
    authenticationProbe: null,
    cancelPending: false,
    config: null,
    eventSource: null,
    locale: {},
    modelActionErrors: new Map(),
    modelActions: new Map(),
    modelEventSources: new Map(),
    modelListError: "",
    modelListRequest: 0,
    modelOrder: [],
    modelStatus: "idle",
    modelVersions: new Map(),
    models: new Map(),
    requiredDownloadPending: false,
    selectedFile: null,
    submitting: false,
  };

  const elements = {};

  function t(key, values) {
    const fallback = state.locale[K.errorGeneric] || "";
    const template = state.locale[key] || fallback;
    return template.replace(/\{([a-z_]+)\}/g, (_, name) => {
      if (!values || !Object.prototype.hasOwnProperty.call(values, name)) {
        return "";
      }
      return String(values[name]);
    });
  }

  function applyI18n(root) {
    root.querySelectorAll("[data-i18n]").forEach((node) => {
      node.textContent = t(node.dataset.i18n);
    });
    root.querySelectorAll("[data-i18n-aria]").forEach((node) => {
      node.setAttribute("aria-label", t(node.dataset.i18nAria));
    });
  }

  function byID(id) {
    return document.getElementById(id);
  }

  function authHeaders() {
    return { "X-Auth-Token": window.__WHISPER_CPP_GUI__.token };
  }

  function clearStoredActiveJob() {
    try {
      window.sessionStorage.removeItem(activeJobStorageKey);
    } catch (_) {
      // Session storage is optional; the current page remains usable without recovery.
    }
  }

  function readStoredActiveJob() {
    try {
      const id = window.sessionStorage.getItem(activeJobStorageKey) || "";
      if (jobIDPattern.test(id)) {
        return id;
      }
      window.sessionStorage.removeItem(activeJobStorageKey);
    } catch (_) {
      // Continue without reload recovery.
    }
    return "";
  }

  function storeActiveJob(id) {
    if (!jobIDPattern.test(id)) {
      clearStoredActiveJob();
      return;
    }
    try {
      window.sessionStorage.setItem(activeJobStorageKey, id);
    } catch (_) {
      // The live page still tracks the job in memory.
    }
  }

  function apiPath(path) {
    return path;
  }

  async function apiFetch(path, options) {
    const response = await fetch(apiPath(path), {
      ...options,
      headers: { ...authHeaders(), ...((options && options.headers) || {}) },
    });
    if (!response.ok) {
      const authenticationFailed = response.status === 401;
      if (authenticationFailed) {
        if (typeof window.__WHISPER_CPP_GUI__.clearStoredToken === "function") {
          window.__WHISPER_CPP_GUI__.clearStoredToken();
        }
        clearStoredActiveJob();
      }
      let code = "";
      try {
        code = (await response.json()).error_code || "";
      } catch (_) {
        code = "";
      }
      const errorCode = authenticationFailed ? "authentication" : code;
      const error = new Error(errorCode || String(response.status));
      error.code = errorCode;
      error.status = response.status;
      throw error;
    }
    return response;
  }

  function probeStreamAuthentication() {
    if (state.authenticationProbe) {
      return state.authenticationProbe;
    }
    const probe = apiFetch("/api/config", { method: "GET" })
      .then((response) => response.text())
      .catch((error) => {
        if (error && error.code === "authentication") {
          closeEvents();
          closeAllModelEvents();
          setConnection(K.connectionError);
          showRequestError(error);
        }
      })
      .finally(() => {
        if (state.authenticationProbe === probe) {
          state.authenticationProbe = null;
        }
      });
    state.authenticationProbe = probe;
    return probe;
  }

  function errorText(code) {
    if (code && state.locale[`screen.error.${code}`]) {
      return t(`screen.error.${code}`);
    }
    return t(K.errorGeneric);
  }

  function setConnection(key) {
    elements.connectionState.textContent = t(key);
  }

  function clearChildren(node) {
    node.replaceChildren();
  }

  function modelPayloadError() {
    const error = new Error("malformed model payload");
    error.code = "malformed_payload";
    return error;
  }

  function normalizeModelDescriptor(value) {
    if (!value || typeof value !== "object" || Array.isArray(value)) {
      return null;
    }
    const name = value.name;
    const filename = value.filename;
    const size = value.size;
    const checksum = value.sha256;
    if (typeof name !== "string" || !modelNamePattern.test(name) ||
        typeof filename !== "string" || !filename || filename === "." || filename === ".." || /[\\/]/.test(filename) ||
        !Number.isSafeInteger(size) || size <= 0 ||
        typeof checksum !== "string" || !/^[a-f0-9]{64}$/.test(checksum)) {
      return null;
    }
    return { name, filename, size, sha256: checksum };
  }

  function modelDescriptorsEqual(left, right) {
    return Boolean(left && right && left.name === right.name && left.filename === right.filename &&
      left.size === right.size && left.sha256 === right.sha256);
  }

  function normalizeModelSnapshot(value, expectedName, expectedDescriptor) {
    if (!value || typeof value !== "object" || Array.isArray(value) ||
        !value.model || typeof value.model !== "object" || Array.isArray(value.model)) {
      return null;
    }
    const descriptor = normalizeModelDescriptor(value.model);
    if (!descriptor) {
      return null;
    }
    const name = descriptor.name;
    const size = descriptor.size;
    const bytesDownloaded = value.bytes_downloaded;
    const errorCode = value.error_code == null ? "" : value.error_code;
    if (!modelStates.has(value.state) ||
        !Number.isSafeInteger(bytesDownloaded) || bytesDownloaded < 0 || bytesDownloaded > size ||
        typeof errorCode !== "string" || errorCode && !modelErrorCodePattern.test(errorCode) ||
        value.state === "downloaded" && (bytesDownloaded !== size || errorCode)) {
      return null;
    }
    if (expectedName && name !== expectedName) {
      return null;
    }
    if (expectedDescriptor && !modelDescriptorsEqual(descriptor, expectedDescriptor)) {
      return null;
    }
    return {
      model: descriptor,
      state: value.state,
      bytes_downloaded: bytesDownloaded,
      error_code: errorCode,
    };
  }

  function normalizeModelManifest(value) {
    if (!Array.isArray(value) || !value.length) {
      throw modelPayloadError();
    }
    const names = new Set();
    const filenames = new Set();
    return value.map((item) => {
      const descriptor = normalizeModelDescriptor(item);
      if (!descriptor || names.has(descriptor.name) || filenames.has(descriptor.filename)) {
        throw modelPayloadError();
      }
      names.add(descriptor.name);
      filenames.add(descriptor.filename);
      return descriptor;
    });
  }

  function normalizeModelsPayload(value, expectedManifest) {
    if (!value || typeof value !== "object" || Array.isArray(value) || !Array.isArray(value.models) ||
        !Array.isArray(expectedManifest) || value.models.length !== expectedManifest.length) {
      throw modelPayloadError();
    }
    return value.models.map((item, index) => {
      const expected = expectedManifest[index];
      const snapshot = normalizeModelSnapshot(item, expected.name, expected);
      if (!snapshot) {
        throw modelPayloadError();
      }
      return snapshot;
    });
  }

  function modelErrorText(code) {
    const key = code && `screen.models.error.${code}`;
    if (key && state.locale[key]) {
      return t(key);
    }
    return t(K.modelErrorGeneric);
  }

  function modelErrorCode(error) {
    return error && typeof error.code === "string" && error.code ? error.code : "request_failed";
  }

  function modelDisplayName(name) {
    const key = `screen.models.name.${name}`;
    return state.locale[key] ? t(key) : name;
  }

  function formatNumber(value, maximumFractionDigits) {
    try {
      return new Intl.NumberFormat(document.documentElement.lang || "ja", {
        maximumFractionDigits,
        minimumFractionDigits: 0,
      }).format(value);
    } catch (_) {
      return String(Math.round(value));
    }
  }

  function formatModelSize(bytes) {
    const units = [
      { divisor: 1024 ** 3, key: K.modelSizeUnitGB },
      { divisor: 1024 ** 2, key: K.modelSizeUnitMB },
      { divisor: 1024, key: K.modelSizeUnitKB },
      { divisor: 1, key: K.modelSizeUnitB },
    ];
    const unit = units.find((candidate) => bytes >= candidate.divisor) || units[units.length - 1];
    const value = bytes / unit.divisor;
    return t(K.modelSizeValue, {
      value: formatNumber(value, unit.divisor === 1 ? 0 : 1),
      unit: t(unit.key),
    });
  }

  function modelProgress(snapshot) {
    const percent = snapshot.model.size > 0
      ? Math.round(snapshot.bytes_downloaded / snapshot.model.size * 100)
      : 0;
    return Math.max(0, Math.min(100, percent));
  }

  function requiredModelNames() {
    const preset = selectedPreset();
    if (!preset || typeof preset.model !== "string" || !modelNamePattern.test(preset.model)) {
      return null;
    }
    const names = [preset.model];
    if (elements.vad.checked) {
      names.push(sileroVADModel);
    }
    return Array.from(new Set(names));
  }

  function isStrictlyDownloaded(snapshot) {
    return Boolean(snapshot && snapshot.state === "downloaded" &&
      snapshot.bytes_downloaded === snapshot.model.size && !snapshot.error_code);
  }

  function requiredModelsDownloaded() {
    const names = requiredModelNames();
    return state.modelStatus === "ready" && Boolean(names) && names.every((name) => {
      return !state.modelActions.has(name) && isStrictlyDownloaded(state.models.get(name));
    });
  }

  function formatModelNames(names) {
    return names.map(modelDisplayName).join(t(K.modelNameSeparator));
  }

  function setRequiredModelNotice(title, message, actionKey) {
    elements.requiredModelNotice.hidden = false;
    elements.requiredModelTitle.textContent = t(title);
    elements.requiredModelMessage.textContent = message;
    elements.requiredModelAction.hidden = !actionKey;
    if (actionKey) {
      elements.requiredModelAction.textContent = t(state.requiredDownloadPending ? K.modelRequiredActionBusy : actionKey);
      elements.requiredModelAction.disabled = state.requiredDownloadPending;
    }
  }

  function renderRequiredModelNotice() {
    if (!state.config || !selectedPreset()) {
      elements.requiredModelNotice.hidden = true;
      return;
    }
    const names = requiredModelNames();
    if (!names || state.modelStatus === "error") {
      setRequiredModelNotice(
        K.modelRequiredUnavailable,
        t(K.modelRequiredUnavailableMessage),
        K.modelRequiredRefresh,
      );
      return;
    }
    if (state.modelStatus !== "ready") {
      setRequiredModelNotice(
        K.modelRequiredChecking,
        t(K.modelRequiredCheckingMessage),
        "",
      );
      return;
    }
    const snapshots = names.map((name) => state.models.get(name));
    if (snapshots.some((snapshot) => !snapshot)) {
      setRequiredModelNotice(
        K.modelRequiredUnavailable,
        t(K.modelRequiredUnavailableMessage),
        K.modelRequiredRefresh,
      );
      return;
    }
    const blocked = snapshots.filter((snapshot) => !isStrictlyDownloaded(snapshot));
    if (!blocked.length) {
      elements.requiredModelNotice.hidden = true;
      return;
    }
    const downloading = blocked.filter((snapshot) => snapshot.state === "downloading" || state.modelActions.get(snapshot.model.name) === "download");
    const downloadable = blocked.filter((snapshot) => ["missing", "invalid"].includes(snapshot.state) && !state.modelActions.has(snapshot.model.name));
    if (downloadable.length) {
      setRequiredModelNotice(
        K.modelRequiredTitle,
        t(K.modelRequiredMessage, { models: formatModelNames(blocked.map((snapshot) => snapshot.model.name)) }),
        K.modelRequiredAction,
      );
      return;
    }
    setRequiredModelNotice(
      K.modelRequiredDownloading,
      t(K.modelRequiredDownloadingMessage, { models: formatModelNames(downloading.map((snapshot) => snapshot.model.name)) }),
      "",
    );
  }

  function createModelAction(snapshot, action, textKey, labelKey, handler) {
    const button = document.createElement("button");
    const name = snapshot.model.name;
    button.className = action === "delete" ? "model-action danger-action" : "model-action";
    button.type = "button";
    button.textContent = t(textKey);
    button.setAttribute("aria-label", t(labelKey, { model: modelDisplayName(name) }));
    button.disabled = Boolean(state.modelActions.has(name) || snapshot.state === "downloading");
    button.addEventListener("click", () => handler(name));
    return button;
  }

  function createModelItem(snapshot, index) {
    const item = document.createElement("li");
    const details = document.createElement("div");
    const headingRow = document.createElement("div");
    const heading = document.createElement("h3");
    const status = document.createElement("p");
    const filename = document.createElement("p");
    const size = document.createElement("p");
    const actions = document.createElement("div");
    const name = snapshot.model.name;
    const pending = state.modelActions.get(name);
    const displayName = modelDisplayName(name);
    const headingID = `model-item-${index}-title`;

    item.className = "model-item";
    item.dataset.state = snapshot.state;
    item.setAttribute("aria-labelledby", headingID);
    details.className = "model-details";
    headingRow.className = "model-heading";
    heading.id = headingID;
    heading.textContent = displayName;
    status.className = "model-state";
    status.textContent = t(K.modelStateLabel, { state: t(`screen.models.state.${snapshot.state}`) });
    filename.className = "model-meta";
    filename.textContent = t(K.modelFilename, { filename: snapshot.model.filename });
    size.className = "model-meta";
    size.textContent = t(K.modelSize, { size: formatModelSize(snapshot.model.size) });
    actions.className = "model-actions";
    headingRow.append(heading, status);
    details.append(headingRow, filename, size);

    if (snapshot.state === "downloading") {
      const progress = document.createElement("progress");
      const progressText = document.createElement("p");
      const percent = modelProgress(snapshot);
      progress.className = "model-progress";
      progress.max = 100;
      progress.value = percent;
      progress.setAttribute("aria-label", t(K.modelProgressLabel, { model: displayName }));
      progress.setAttribute("aria-valuetext", t(K.modelProgressValue, {
        percent,
        downloaded: formatModelSize(snapshot.bytes_downloaded),
        total: formatModelSize(snapshot.model.size),
      }));
      progressText.className = "model-progress-text";
      progressText.textContent = t(K.modelProgressValue, {
        percent,
        downloaded: formatModelSize(snapshot.bytes_downloaded),
        total: formatModelSize(snapshot.model.size),
      });
      details.append(progress, progressText);
    }

    const errorCode = snapshot.error_code || state.modelActionErrors.get(name);
    if (errorCode) {
      const error = document.createElement("p");
      error.className = "model-item-error";
      error.setAttribute("role", "status");
      error.textContent = modelErrorText(errorCode);
      details.append(error);
    }

    if (["missing", "invalid", "downloading"].includes(snapshot.state)) {
      let textKey = K.modelActionDownload;
      let labelKey = K.modelActionDownloadLabel;
      if (snapshot.state === "downloading" || pending === "download") {
        textKey = K.modelActionDownloading;
      } else if (errorCode) {
        textKey = K.modelActionRetry;
        labelKey = K.modelActionRetryLabel;
      } else if (snapshot.state === "invalid") {
        textKey = K.modelActionRedownload;
        labelKey = K.modelActionRedownloadLabel;
      }
      actions.append(createModelAction(snapshot, "download", textKey, labelKey, startModelDownload));
    }
    if (["downloaded", "invalid"].includes(snapshot.state)) {
      actions.append(createModelAction(
        snapshot,
        "delete",
        pending === "delete" ? K.modelActionDeleting : K.modelActionDelete,
        K.modelActionDeleteLabel,
        deleteModel,
      ));
    }
    item.append(details, actions);
    return item;
  }

  function renderModels() {
    const snapshots = state.modelOrder.map((name) => state.models.get(name)).filter(Boolean);
    const downloaded = snapshots.filter(isStrictlyDownloaded).length;
    elements.modelRefresh.disabled = ["idle", "loading"].includes(state.modelStatus);
    elements.modelList.setAttribute("aria-busy", String(state.modelStatus === "loading" || state.modelStatus === "idle"));
    if (state.modelStatus === "ready") {
      elements.modelSummary.textContent = t(K.modelSummaryReady, { downloaded, total: snapshots.length });
    } else if (state.modelStatus === "error") {
      elements.modelSummary.textContent = t(K.modelSummaryError);
    } else {
      elements.modelSummary.textContent = t(K.modelLoading);
    }
    elements.modelErrorPanel.hidden = state.modelStatus !== "error";
    elements.modelErrorMessage.textContent = state.modelStatus === "error" ? modelErrorText(state.modelListError) : "";

    clearChildren(elements.modelList);
    if (!snapshots.length) {
      const empty = document.createElement("li");
      empty.className = "model-list-empty";
      empty.textContent = t(state.modelStatus === "ready" ? K.modelEmpty : state.modelStatus === "error" ? K.modelSummaryError : K.modelLoading);
      elements.modelList.append(empty);
      return;
    }
    snapshots.forEach((snapshot, index) => elements.modelList.append(createModelItem(snapshot, index)));
  }

  function renderModelDependentUI() {
    renderModels();
    renderRequiredModelNotice();
    syncControls();
  }

  function closeModelEvents(name) {
    const source = state.modelEventSources.get(name);
    if (source) {
      source.close();
      state.modelEventSources.delete(name);
    }
    state.modelVersions.set(name, (state.modelVersions.get(name) || 0) + 1);
  }

  function closeAllModelEvents() {
    Array.from(state.modelEventSources.keys()).forEach(closeModelEvents);
  }

  function setModelSnapshot(snapshot) {
    const name = snapshot && snapshot.model && snapshot.model.name;
    const current = name && state.models.get(name);
    const normalized = current && normalizeModelSnapshot(snapshot, name, current.model);
    if (!normalized) {
      return false;
    }
    state.models.set(name, normalized);
    if (normalized.error_code) {
      state.modelActionErrors.delete(name);
    }
    if (normalized.state !== "downloading") {
      closeModelEvents(name);
    }
    renderModelDependentUI();
    return true;
  }

  function parseModelEventData(name, source, version, message) {
    if (state.modelEventSources.get(name) !== source || state.modelVersions.get(name) !== version) {
      return null;
    }
    try {
      return JSON.parse(message.data);
    } catch (_) {
      state.modelActionErrors.set(name, "malformed_payload");
      closeModelEvents(name);
      renderModelDependentUI();
      return null;
    }
  }

  function openModelEvents(name) {
    const snapshot = state.models.get(name);
    if (!snapshot || snapshot.state !== "downloading") {
      closeModelEvents(name);
      return;
    }
    closeModelEvents(name);
    const version = (state.modelVersions.get(name) || 0) + 1;
    state.modelVersions.set(name, version);
    const token = encodeURIComponent(window.__WHISPER_CPP_GUI__.token);
    const source = new EventSource(`/api/models/${encodeURIComponent(name)}/events?token=${token}`);
    state.modelEventSources.set(name, source);
    source.addEventListener("open", () => {
      if (state.modelEventSources.get(name) === source && state.modelActionErrors.get(name) === "stream_error") {
        state.modelActionErrors.delete(name);
        renderModelDependentUI();
      }
    });
    source.addEventListener("snapshot", (message) => {
      const value = parseModelEventData(name, source, version, message);
      const current = state.models.get(name);
      const normalized = value && current && normalizeModelSnapshot(value, name, current.model);
      if (!normalized) {
        if (value) {
          state.modelActionErrors.set(name, "malformed_payload");
          closeModelEvents(name);
          renderModelDependentUI();
        }
        return;
      }
      setModelSnapshot(normalized);
    });
    source.addEventListener("progress", (message) => {
      const value = parseModelEventData(name, source, version, message);
      const current = state.models.get(name);
      if (!value || !current || current.state !== "downloading" || value.name !== name ||
          !Number.isSafeInteger(value.bytes_downloaded) || value.bytes_downloaded < 0 ||
          value.bytes_downloaded > current.model.size) {
        if (value) {
          state.modelActionErrors.set(name, "malformed_payload");
          closeModelEvents(name);
          renderModelDependentUI();
        }
        return;
      }
      state.models.set(name, {
        ...current,
        bytes_downloaded: Math.max(current.bytes_downloaded, value.bytes_downloaded),
      });
      renderModelDependentUI();
    });
    source.addEventListener("error", () => {
      if (state.modelEventSources.get(name) === source && state.models.get(name)?.state === "downloading") {
        state.modelActionErrors.set(name, "stream_error");
        renderModelDependentUI();
        probeStreamAuthentication();
      }
    });
  }

  function synchronizeModelEvents() {
    for (const name of Array.from(state.modelEventSources.keys())) {
      const snapshot = state.models.get(name);
      if (!snapshot || snapshot.state !== "downloading") {
        closeModelEvents(name);
      }
    }
    for (const name of state.modelOrder) {
      const snapshot = state.models.get(name);
      if (snapshot && snapshot.state === "downloading" && !state.modelEventSources.has(name)) {
        openModelEvents(name);
      }
    }
  }

  async function loadModels() {
    const request = ++state.modelListRequest;
    state.modelStatus = "loading";
    state.modelListError = "";
    renderModelDependentUI();
    try {
      const response = await apiFetch("/api/models", { method: "GET" });
      const snapshots = normalizeModelsPayload(await response.json(), state.config && state.config.models);
      if (request !== state.modelListRequest) {
        return false;
      }
      closeAllModelEvents();
      state.models = new Map(snapshots.map((snapshot) => [snapshot.model.name, snapshot]));
      state.modelOrder = snapshots.map((snapshot) => snapshot.model.name);
      state.modelActionErrors.clear();
      state.modelStatus = "ready";
      renderModelDependentUI();
      synchronizeModelEvents();
      return true;
    } catch (error) {
      if (request !== state.modelListRequest) {
        return false;
      }
      closeAllModelEvents();
      state.models.clear();
      state.modelOrder = [];
      state.modelStatus = "error";
      state.modelListError = modelErrorCode(error);
      renderModelDependentUI();
      return false;
    }
  }

  async function startModelDownload(name) {
    const snapshot = state.models.get(name);
    if (!snapshot || state.modelActions.has(name) || snapshot.state === "downloading" ||
        !["missing", "invalid"].includes(snapshot.state)) {
      return false;
    }
    state.modelActions.set(name, "download");
    state.modelActionErrors.delete(name);
    renderModelDependentUI();
    try {
      const response = await apiFetch(`/api/models/${encodeURIComponent(name)}/download`, { method: "POST" });
      const normalized = normalizeModelSnapshot(await response.json(), name, snapshot.model);
      if (!normalized) {
        throw modelPayloadError();
      }
      state.models.set(name, normalized);
      if (normalized.state === "downloading") {
        openModelEvents(name);
      }
      return true;
    } catch (error) {
      const code = modelErrorCode(error);
      if (code === "download_in_progress") {
        await loadModels();
      } else {
        state.modelActionErrors.set(name, code);
      }
      return false;
    } finally {
      if (state.modelActions.get(name) === "download") {
        state.modelActions.delete(name);
      }
      renderModelDependentUI();
    }
  }

  async function deleteModel(name) {
    const snapshot = state.models.get(name);
    if (!snapshot || state.modelActions.has(name) || !["downloaded", "invalid"].includes(snapshot.state)) {
      return;
    }
    if (!window.confirm(t(K.modelActionDeleteConfirm, { model: modelDisplayName(name) }))) {
      return;
    }
    state.modelActions.set(name, "delete");
    state.modelActionErrors.delete(name);
    renderModelDependentUI();
    try {
      await apiFetch(`/api/models/${encodeURIComponent(name)}`, { method: "DELETE" });
      await loadModels();
    } catch (error) {
      state.modelActionErrors.set(name, modelErrorCode(error));
    } finally {
      if (state.modelActions.get(name) === "delete") {
        state.modelActions.delete(name);
      }
      renderModelDependentUI();
    }
  }

  async function downloadRequiredModels() {
    if (state.modelStatus === "error") {
      await loadModels();
      return;
    }
    const names = requiredModelNames();
    if (!names || state.requiredDownloadPending) {
      return;
    }
    const downloadable = names.filter((name) => {
      const snapshot = state.models.get(name);
      return snapshot && ["missing", "invalid"].includes(snapshot.state) && !state.modelActions.has(name);
    });
    if (!downloadable.length) {
      return;
    }
    state.requiredDownloadPending = true;
    renderModelDependentUI();
    try {
      await Promise.all(downloadable.map(startModelDownload));
    } finally {
      state.requiredDownloadPending = false;
      renderModelDependentUI();
    }
  }

  function selectedPreset() {
    const input = elements.presetOptions.querySelector("input[name=preset]:checked");
    return input && state.config.presets.find((preset) => preset.id === input.value);
  }

  function setSelectedFile(file) {
    state.selectedFile = file || null;
    elements.selectedFile.textContent = state.selectedFile
      ? t(K.filenameSelected, { filename: state.selectedFile.name })
      : t(K.filenameEmpty);
    syncControls();
  }

  function selectPresetDefaults() {
    const preset = selectedPreset();
    if (!preset) {
      renderRequiredModelNotice();
      syncControls();
      return;
    }
    elements.vad.checked = Boolean(preset.vad);
    elements.outputOptions.querySelectorAll("input[type=checkbox]").forEach((input) => {
      input.checked = Array.isArray(preset.outputs) && preset.outputs.includes(input.value);
    });
    renderRequiredModelNotice();
    syncControls();
  }

  function renderPresets() {
    clearChildren(elements.presetOptions);
    const presets = state.config.presets;
    if (!presets.length) {
      const empty = document.createElement("p");
      empty.textContent = t(K.presetEmpty);
      elements.presetOptions.append(empty);
      return;
    }
    presets.forEach((preset, index) => {
      const label = document.createElement("label");
      const input = document.createElement("input");
      const content = document.createElement("span");
      const name = document.createElement("span");
      const meta = document.createElement("span");

      label.className = "preset-option";
      input.name = "preset";
      input.type = "radio";
      input.value = preset.id;
      input.checked = index === 0;
      input.addEventListener("change", selectPresetDefaults);
      name.textContent = t(preset.name, { name: preset.id });
      meta.className = "preset-meta";
      meta.textContent = t("screen.preset.option.meta", { model: preset.model });
      content.append(name, meta);
      label.append(input, content);
      elements.presetOptions.append(label);
    });
    selectPresetDefaults();
  }

  function renderSetup() {
    clearChildren(elements.setupList);
    const missing = Object.entries(state.config.tools).filter(([, tool]) => !tool.found);
    elements.setupPanel.hidden = missing.length === 0;
    missing.forEach(([name, tool]) => {
      const item = document.createElement("li");
      const content = document.createElement("div");
      const missingText = document.createElement("p");
      const command = document.createElement("code");
      const copy = document.createElement("button");
      const value = t(K.setupCommand, { package: tool.brew_package });

      const toolNameKey = toolNameKeys[name];
      missingText.textContent = t(K.setupMissing, { tool: toolNameKey ? t(toolNameKey) : name });
      command.className = "command";
      command.textContent = value;
      copy.type = "button";
      copy.textContent = t(K.setupCopy);
      copy.addEventListener("click", () => copyCommand(value));
      content.append(missingText, command);
      item.append(content, copy);
      elements.setupList.append(item);
    });
  }

  async function copyCommand(value) {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(value);
      } else {
        const field = document.createElement("textarea");
        field.value = value;
        field.setAttribute("aria-hidden", "true");
        document.body.append(field);
        field.select();
        document.execCommand("copy");
        field.remove();
      }
      elements.copyStatus.textContent = t(K.copySuccess);
    } catch (_) {
      elements.copyStatus.textContent = t(K.copyError);
    }
  }

  function isTerminal(status) {
    return ["done", "failed", "cancelled"].includes(status);
  }

  function renderLogs(logs) {
    clearChildren(elements.logList);
    if (!logs.length) {
      const item = document.createElement("li");
      item.className = "log-empty";
      item.textContent = t(K.logEmpty);
      elements.logList.append(item);
      return;
    }
    logs.forEach((entry) => appendLog(entry));
  }

  function appendLog(entry) {
    if (!entry || typeof entry.line !== "string") {
      return;
    }
    const empty = elements.logList.querySelector(".log-empty");
    if (empty) {
      empty.remove();
    }
    const item = document.createElement("li");
    item.textContent = entry.line;
    elements.logList.append(item);
  }

  function renderDownloads(outputs) {
    clearChildren(elements.downloadList);
    elements.downloadPanel.hidden = !outputs.length;
    outputs.forEach((file) => {
      const item = document.createElement("li");
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = t(K.downloadAction, { file });
      button.addEventListener("click", () => downloadOutput(file));
      item.append(button);
      elements.downloadList.append(item);
    });
  }

  async function downloadOutput(file) {
    if (!state.activeJob) {
      return;
    }
    const jobID = state.activeJob.id;
    try {
      const path = `/api/download/${encodeURIComponent(jobID)}/${encodeURIComponent(file)}`;
      const response = await apiFetch(path, { method: "GET" });
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = file;
      link.hidden = true;
      document.body.append(link);
      link.click();
      link.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 0);
    } catch (error) {
      showRequestError(error);
    }
  }

  function renderError(snapshot) {
    const failed = snapshot && snapshot.status === "failed";
    const recoveryPath = failed && typeof snapshot.recovery_path === "string" ? snapshot.recovery_path : "";
    elements.errorPanel.hidden = !failed;
    if (!failed) {
      elements.errorMessage.textContent = "";
      elements.recoveryPath.textContent = "";
      elements.recoveryPath.hidden = true;
      elements.stderrTail.textContent = "";
      elements.stderrTail.hidden = true;
      return;
    }
    elements.errorMessage.textContent = errorText(snapshot.error_code);
    elements.recoveryPath.textContent = recoveryPath ? t(K.recoveryPath, { path: recoveryPath }) : "";
    elements.recoveryPath.hidden = !recoveryPath;
    elements.stderrTail.textContent = snapshot.stderr_tail || "";
    elements.stderrTail.hidden = !elements.stderrTail.textContent;
  }

  function renderJob() {
    const job = state.activeJob;
    if (!job) {
      elements.jobStatus.textContent = t(K.statusIdle);
      elements.jobProgress.removeAttribute("value");
      elements.progressLabel.textContent = t(K.progressUnknown);
      elements.cancelButton.hidden = true;
      renderLogs([]);
      renderError(null);
      renderDownloads([]);
      syncControls();
      return;
    }
    const statusKey = `screen.job.status.${job.status}`;
    const phaseKey = job.phase && `screen.job.phase.${job.phase}`;
    elements.jobStatus.textContent = [t(statusKey), phaseKey ? t(phaseKey) : ""].filter(Boolean).join(" ");
    if (typeof job.progress === "number") {
      elements.jobProgress.value = job.progress;
      elements.jobProgress.setAttribute("aria-valuetext", t(K.progressValue, { value: job.progress }));
      elements.progressLabel.textContent = t(K.progressValue, { value: job.progress });
    } else {
      elements.jobProgress.removeAttribute("value");
      elements.jobProgress.removeAttribute("aria-valuetext");
      elements.progressLabel.textContent = t(K.progressUnknown);
    }
    elements.cancelButton.hidden = isTerminal(job.status);
    renderError(job);
    syncControls();
  }

  function syncControls() {
    const usable = state.config && state.config.presets.length > 0 && !Object.values(state.config.tools).some((tool) => !tool.found);
    const jobBusy = Boolean(state.activeJob && !isTerminal(state.activeJob.status));
    const busy = state.submitting || jobBusy;
    const outputsSelected = Boolean(elements.outputOptions.querySelector("input[type=checkbox]:checked"));
    const modelsReady = requiredModelsDownloaded();

    elements.jobControls.disabled = !usable;
    [
      elements.dropZone,
      elements.fileInput,
      elements.filePicker,
      elements.vad,
      ...elements.presetOptions.querySelectorAll("input"),
      ...elements.outputOptions.querySelectorAll("input"),
    ].forEach((control) => {
      control.disabled = !usable || busy;
    });
    elements.startButton.disabled = !usable || !state.selectedFile || !selectedPreset() || !outputsSelected || !modelsReady || busy;
    elements.startButton.textContent = t(busy ? K.startBusy : K.startIdle);
    elements.cancelButton.disabled = !jobBusy || state.cancelPending;
    elements.cancelButton.textContent = t(state.cancelPending ? K.cancelBusy : K.cancelIdle);
  }

  function snapshotFrom(value) {
    return {
      ...value,
      logs: Array.isArray(value.logs) ? value.logs : [],
      outputs: Array.isArray(value.outputs) ? value.outputs : [],
      progress: typeof value.progress === "number" ? value.progress : null,
    };
  }

  function applyActiveJobSnapshot(value) {
    state.activeJob = snapshotFrom(value);
    storeActiveJob(state.activeJob.id);
    renderLogs(state.activeJob.logs);
    renderDownloads(state.activeJob.outputs);
    renderJob();
  }

  function consumeSnapshot(value) {
    applyActiveJobSnapshot(value);
    if (isTerminal(state.activeJob.status)) {
      closeEvents();
    }
  }

  function consumeEvent(event) {
    if (!state.activeJob || event.job_id !== state.activeJob.id) {
      return;
    }
    if (event.type === "state") {
      state.activeJob.status = event.status || state.activeJob.status;
      state.activeJob.phase = event.phase || "";
      state.activeJob.error_code = event.error_code || state.activeJob.error_code;
    }
    if (event.type === "progress") {
      state.activeJob.progress = typeof event.progress === "number" ? event.progress : null;
    }
    if (event.type === "log" && event.log) {
      state.activeJob.logs.push(event.log);
      appendLog(event.log);
    }
    renderJob();
  }

  function openEvents(id) {
    closeEvents();
    const token = encodeURIComponent(window.__WHISPER_CPP_GUI__.token);
    const source = new EventSource(`/api/jobs/${encodeURIComponent(id)}/events?token=${token}`);
    state.eventSource = source;
    source.addEventListener("open", () => {
      if (state.eventSource === source) {
        setConnection(K.connectionReady);
      }
    });
    source.addEventListener("snapshot", (message) => {
      const value = parseEventData(source, message);
      if (value) {
        consumeSnapshot(value);
      }
    });
    ["state", "progress", "log"].forEach((type) => {
      source.addEventListener(type, (message) => {
        const value = parseEventData(source, message);
        if (value) {
          consumeEvent(value);
        }
      });
    });
    source.addEventListener("error", () => {
      if (state.eventSource === source && state.activeJob && !isTerminal(state.activeJob.status)) {
        setConnection(K.connectionReconnecting);
        probeStreamAuthentication();
      }
    });
  }

  function parseEventData(source, message) {
    if (state.eventSource !== source) {
      return null;
    }
    try {
      return JSON.parse(message.data);
    } catch (_) {
      setConnection(K.connectionError);
      return null;
    }
  }

  function closeEvents() {
    if (state.eventSource) {
      state.eventSource.close();
      state.eventSource = null;
    }
  }

  function waitForRestoreRetry(attempt) {
    const delay = restoreRetryBaseDelay * (2 ** attempt);
    return new Promise((resolve) => window.setTimeout(resolve, delay));
  }

  function transientRestoreError(error) {
    return !error || typeof error.status !== "number" || [429, 500, 502, 503, 504].includes(error.status);
  }

  async function restoreActiveJob() {
    const jobID = readStoredActiveJob();
    if (!jobID) {
      return;
    }
    for (let attempt = 0; attempt < restoreAttemptLimit; attempt += 1) {
      try {
        const response = await apiFetch(`/api/jobs/${encodeURIComponent(jobID)}/outputs`, { method: "GET" });
        const value = await response.json();
        if (!value || value.id !== jobID || typeof value.status !== "string" || !Array.isArray(value.outputs)) {
          clearStoredActiveJob();
          return;
        }
        applyActiveJobSnapshot(value);
        openEvents(jobID);
        return;
      } catch (error) {
        if (error && error.status === 404) {
          clearStoredActiveJob();
          return;
        }
        if (error && error.code === "authentication") {
          setConnection(K.connectionError);
          showRequestError(error);
          return;
        }
        if (!transientRestoreError(error) || attempt === restoreAttemptLimit - 1) {
          setConnection(K.connectionError);
          showRequestError(error);
          return;
        }
        setConnection(K.connectionReconnecting);
        await waitForRestoreRetry(attempt);
        if (readStoredActiveJob() !== jobID || state.activeJob) {
          return;
        }
      }
    }
  }

  function showRequestError(error) {
    const code = error && error.code;
    elements.errorPanel.hidden = false;
    elements.errorMessage.textContent = errorText(code || "request_failed");
    elements.recoveryPath.textContent = "";
    elements.recoveryPath.hidden = true;
    elements.stderrTail.textContent = "";
    elements.stderrTail.hidden = true;
  }

  async function submitJob(event) {
    event.preventDefault();
    const preset = selectedPreset();
    const outputs = Array.from(elements.outputOptions.querySelectorAll("input:checked"), (input) => input.value);
    if (state.submitting || !state.selectedFile || !preset || !outputs.length || !requiredModelsDownloaded() ||
        state.activeJob && !isTerminal(state.activeJob.status)) {
      return;
    }
    const form = new FormData();
    form.append("preset", preset.id);
    form.append("file", state.selectedFile, state.selectedFile.name);
    form.append("options", JSON.stringify({
      vad: elements.vad.checked,
      outputs,
    }));
    state.submitting = true;
    renderError(null);
    elements.dropZone.textContent = t("screen.upload.drop.busy");
    syncControls();
    try {
      const response = await apiFetch("/api/jobs", { method: "POST", body: form });
      const result = await response.json();
      applyActiveJobSnapshot({ id: result.id, status: result.status, logs: [], outputs: [] });
      openEvents(result.id);
    } catch (error) {
      showRequestError(error);
      if (error && ["model_missing", "vad_model_missing"].includes(error.code)) {
        loadModels();
      }
    } finally {
      state.submitting = false;
      elements.dropZone.textContent = t("screen.upload.drop.idle");
      syncControls();
    }
  }

  async function cancelJob() {
    if (!state.activeJob || isTerminal(state.activeJob.status)) {
      return;
    }
    const jobID = state.activeJob.id;
    state.cancelPending = true;
    syncControls();
    try {
      await apiFetch(`/api/jobs/${encodeURIComponent(jobID)}/cancel`, { method: "POST" });
    } catch (error) {
      showRequestError(error);
    } finally {
      state.cancelPending = false;
      syncControls();
    }
  }

  async function loadConfig() {
    try {
      const response = await apiFetch("/api/config", { method: "GET" });
      const value = await response.json();
      if (!value || !Array.isArray(value.presets) || !value.tools) {
        throw Object.assign(new Error("configuration"), { code: "configuration" });
      }
      const models = normalizeModelManifest(value.models);
      const modelNames = new Set(models.map((descriptor) => descriptor.name));
      if (!modelNames.has(sileroVADModel) || value.presets.some((preset) => {
        return !preset || typeof preset !== "object" || !modelNames.has(preset.model);
      })) {
        throw Object.assign(new Error("configuration"), { code: "configuration" });
      }
      state.config = { presets: value.presets, tools: value.tools, models };
      renderPresets();
      renderSetup();
      setConnection(K.connectionReady);
      syncControls();
      return true;
    } catch (error) {
      setConnection(K.connectionError);
      showRequestError(error && error.code ? error : { code: "configuration" });
      return false;
    }
  }

  function bindElements() {
    Object.assign(elements, {
      cancelButton: byID("cancel-button"),
      connectionState: byID("connection-state"),
      copyStatus: byID("copy-status"),
      dropZone: byID("drop-zone"),
      errorMessage: byID("error-message"),
      errorPanel: byID("error-panel"),
      fileInput: byID("file-input"),
      filePicker: byID("file-picker"),
      jobControls: byID("job-controls"),
      jobForm: byID("job-form"),
      jobProgress: byID("job-progress"),
      jobStatus: byID("job-status"),
      logList: byID("log-list"),
      modelErrorMessage: byID("model-error-message"),
      modelErrorPanel: byID("model-error-panel"),
      modelList: byID("model-list"),
      modelRefresh: byID("model-refresh"),
      modelSummary: byID("model-summary"),
      outputOptions: byID("output-options"),
      presetOptions: byID("preset-options"),
      progressLabel: byID("progress-label"),
      recoveryPath: byID("recovery-path"),
      requiredModelAction: byID("required-model-action"),
      requiredModelMessage: byID("required-model-message"),
      requiredModelNotice: byID("required-model-notice"),
      requiredModelTitle: byID("required-model-title"),
      selectedFile: byID("selected-file"),
      setupList: byID("setup-list"),
      setupPanel: byID("setup-panel"),
      startButton: byID("start-button"),
      stderrTail: byID("stderr-tail"),
      vad: byID("vad-option"),
      downloadList: byID("download-list"),
      downloadPanel: byID("download-panel"),
    });
  }

  function bindInteractions() {
    elements.filePicker.addEventListener("click", () => {
      if (!elements.filePicker.disabled) {
        elements.fileInput.click();
      }
    });
    elements.dropZone.addEventListener("click", () => {
      if (!elements.dropZone.disabled) {
        elements.fileInput.click();
      }
    });
    elements.fileInput.addEventListener("change", () => setSelectedFile(elements.fileInput.files[0]));
    elements.dropZone.addEventListener("dragover", (event) => {
      event.preventDefault();
      if (!elements.dropZone.disabled) {
        elements.dropZone.classList.add("is-dragging");
      }
    });
    elements.dropZone.addEventListener("dragleave", () => elements.dropZone.classList.remove("is-dragging"));
    elements.dropZone.addEventListener("drop", (event) => {
      event.preventDefault();
      elements.dropZone.classList.remove("is-dragging");
      if (!elements.dropZone.disabled) {
        setSelectedFile(event.dataTransfer.files[0]);
      }
    });
    elements.outputOptions.addEventListener("change", syncControls);
    elements.vad.addEventListener("change", () => {
      renderRequiredModelNotice();
      syncControls();
    });
    elements.requiredModelAction.addEventListener("click", downloadRequiredModels);
    elements.modelRefresh.addEventListener("click", loadModels);
    elements.jobForm.addEventListener("submit", submitJob);
    elements.cancelButton.addEventListener("click", cancelJob);
  }

  function localeCandidates() {
    const requested = navigator.languages && navigator.languages.length
      ? Array.from(navigator.languages)
      : [navigator.language];
    const candidates = requested
      .map((value) => String(value || "").split("-")[0].toLowerCase())
      .filter((value) => /^[a-z]{2,3}$/.test(value));
    candidates.push("ja");
    return Array.from(new Set(candidates));
  }

  async function loadLocale() {
    for (const name of localeCandidates()) {
      try {
        const response = await fetch(`/locales/${name}.json`, { cache: "no-store" });
        if (!response.ok) {
          continue;
        }
        const locale = await response.json();
        if (!locale || typeof locale !== "object" || Array.isArray(locale)) {
          continue;
        }
        state.locale = locale;
        document.documentElement.lang = name;
        return;
      } catch (_) {
        // Try the next preferred locale, ending with the embedded Japanese fallback.
      }
    }
    throw new Error("locale");
  }

  async function start() {
    bindElements();
    await loadLocale();
    applyI18n(document);
    renderModels();
    renderJob();
    bindInteractions();
    if (!window.__WHISPER_CPP_GUI__ || !window.__WHISPER_CPP_GUI__.token) {
      setConnection(K.connectionError);
      showRequestError({ code: "authentication" });
      return;
    }
    if (await loadConfig()) {
      await restoreActiveJob();
      await loadModels();
    }
  }

  document.addEventListener("DOMContentLoaded", () => {
    start().catch(() => {
      if (elements.connectionState) {
        setConnection(K.connectionError);
      }
    });
  });
  window.addEventListener("beforeunload", () => {
    closeEvents();
    closeAllModelEvents();
  });
})();
