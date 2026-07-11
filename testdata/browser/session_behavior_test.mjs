import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

const repoRoot = new URL("../../", import.meta.url);
const bootstrapSource = await readFile(new URL("web/bootstrap.js", repoRoot), "utf8");
const appSource = await readFile(new URL("web/app.js", repoRoot), "utf8");

const authStorageKey = "whisper-cpp-gui.auth-token";
const activeJobStorageKey = "whisper-cpp-gui.active-job";
const token = "a".repeat(64);
const jobID = `job-${"b".repeat(32)}`;

function createStorage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getItem(key) {
      return values.has(key) ? values.get(key) : null;
    },
    removeItem(key) {
      values.delete(key);
    },
    setItem(key, value) {
      values.set(key, String(value));
    },
  };
}

function runBootstrap(hash, storage) {
  const location = { hash, pathname: "/", search: "" };
  const replacements = [];
  const window = {
    history: {
      replaceState(_state, _title, path) {
        replacements.push(path);
        location.hash = "";
      },
    },
    location,
    sessionStorage: storage,
  };
  vm.runInNewContext(bootstrapSource, { window });
  return { bootstrap: window.__WHISPER_CPP_GUI__, location, replacements };
}

test("bootstrap restores a validated token across same-tab reloads", () => {
  const storage = createStorage();
  const initial = runBootstrap(`#token=${token}`, storage);
  assert.equal(initial.bootstrap.token, token);
  assert.equal(initial.location.hash, "");
  assert.deepEqual(initial.replacements, ["/"]);
  assert.equal(storage.getItem(authStorageKey), token);

  const reloaded = runBootstrap("", storage);
  assert.equal(reloaded.bootstrap.token, token);
  assert.equal(storage.getItem(authStorageKey), token);
});

test("an invalid fragment preserves an already validated stored token", () => {
  const storage = createStorage({ [authStorageKey]: token });
  const result = runBootstrap("#token=invalid", storage);
  assert.equal(result.bootstrap.token, token);
  assert.equal(result.location.hash, "");
  assert.equal(storage.getItem(authStorageKey), token);
});

test("invalid stored authentication is removed", () => {
  const storage = createStorage({ [authStorageKey]: "invalid" });
  const result = runBootstrap("", storage);
  assert.equal(result.bootstrap.token, "");
  assert.equal(storage.getItem(authStorageKey), null);
});

class MockElement {
  constructor(tagName = "div") {
    this.attributes = new Map();
    this.children = [];
    this.className = "";
    this.classList = {
      add() {},
      remove() {},
    };
    this.dataset = {};
    this.disabled = false;
    this.files = [];
    this.hidden = false;
    this.listeners = new Map();
    this.tagName = tagName.toUpperCase();
    this.textContent = "";
    this.value = "";
  }

  addEventListener(type, callback) {
    const listeners = this.listeners.get(type) || [];
    listeners.push(callback);
    this.listeners.set(type, listeners);
  }

  append(...nodes) {
    this.children.push(...nodes);
  }

  click() {}

  querySelector(selector) {
    if (selector === ".log-empty") {
      return this.children.find((child) => child.className === "log-empty") || null;
    }
    return null;
  }

  querySelectorAll() {
    return [];
  }

  remove() {}

  removeAttribute(name) {
    this.attributes.delete(name);
  }

  replaceChildren(...nodes) {
    this.children = [...nodes];
  }

  select() {}

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }
}

function createDocument() {
  const elements = new Map();
  const listeners = new Map();
  return {
    body: new MockElement("body"),
    createElement(tagName) {
      return new MockElement(tagName);
    },
    documentElement: { lang: "ja" },
    execCommand() {
      return true;
    },
    getElementById(id) {
      if (!elements.has(id)) {
        elements.set(id, new MockElement());
      }
      return elements.get(id);
    },
    addEventListener(type, callback) {
      const callbacks = listeners.get(type) || [];
      callbacks.push(callback);
      listeners.set(type, callbacks);
    },
    querySelectorAll() {
      return [];
    },
    dispatch(type) {
      for (const callback of listeners.get(type) || []) {
        callback();
      }
    },
  };
}

function makeResponse(status, payload) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async blob() {
      return payload;
    },
    async json() {
      return payload;
    },
    async text() {
      return typeof payload === "string" ? payload : JSON.stringify(payload);
    },
  };
}

function errorPayload(status) {
  if (status === 401) {
    return { error_code: "authentication" };
  }
  if (status === 404) {
    return { error_code: "job_not_found" };
  }
  if (status === 429) {
    return { error_code: "subscriber_limit" };
  }
  return { error_code: "manager_unavailable" };
}

function createEventSourceClass() {
  return class MockEventSource {
    static instances = [];

    constructor(url) {
      this.closed = false;
      this.listeners = new Map();
      this.url = url;
      MockEventSource.instances.push(this);
    }

    addEventListener(type, callback) {
      const listeners = this.listeners.get(type) || [];
      listeners.push(callback);
      this.listeners.set(type, listeners);
    }

    close() {
      this.closed = true;
    }

    emit(type, value = {}) {
      for (const callback of this.listeners.get(type) || []) {
        callback(value);
      }
    }
  };
}

async function waitFor(predicate, message) {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (predicate()) {
      return;
    }
    await new Promise((resolve) => setImmediate(resolve));
  }
  assert.fail(message);
}

async function launchApp({
  configStatus = 200,
  jobOutputs = [],
  jobStatus = "running",
  outputStatuses = [200],
  storedJobID = jobID,
} = {}) {
  const storedValues = { [authStorageKey]: token };
  if (storedJobID !== null) {
    storedValues[activeJobStorageKey] = storedJobID;
  }
  const storage = createStorage(storedValues);
  const document = createDocument();
  const EventSource = createEventSourceClass();
  const fetchCalls = [];
  const retryDelays = [];
  let configCallCount = 0;
  let outputCallCount = 0;
  let modelCallCount = 0;
  const descriptor = {
    filename: "ggml-silero-v6.2.0.bin",
    name: "silero-vad",
    sha256: "c".repeat(64),
    size: 1,
  };

  async function fetch(path, options = {}) {
    fetchCalls.push({ path, options });
    if (path.startsWith("/locales/")) {
      return makeResponse(200, {});
    }
    if (path === "/api/config") {
      configCallCount += 1;
      if (configStatus !== 200) {
        return makeResponse(configStatus, errorPayload(configStatus));
      }
      return makeResponse(200, { models: [descriptor], presets: [], tools: {} });
    }
    if (path === "/api/models") {
      modelCallCount += 1;
      return makeResponse(200, {
        models: [{ bytes_downloaded: 0, error_code: "", model: descriptor, state: "missing" }],
      });
    }
    if (path === `/api/jobs/${jobID}/outputs`) {
      if (!outputStatuses.length) {
        throw new Error("unexpected active-job lookup");
      }
      const status = outputStatuses[Math.min(outputCallCount, outputStatuses.length - 1)];
      outputCallCount += 1;
      if (status !== 200) {
        return makeResponse(status, errorPayload(status));
      }
      return makeResponse(200, { id: jobID, outputs: jobOutputs, status: jobStatus });
    }
    throw new Error(`unexpected fetch path: ${path}`);
  }

  const windowListeners = new Map();
  const window = {
    __WHISPER_CPP_GUI__: Object.freeze({
      clearStoredToken() {
        storage.removeItem(authStorageKey);
      },
      token,
    }),
    addEventListener(type, callback) {
      const listeners = windowListeners.get(type) || [];
      listeners.push(callback);
      windowListeners.set(type, listeners);
    },
    confirm() {
      return true;
    },
    sessionStorage: storage,
    setTimeout(callback, delay) {
      retryDelays.push(delay);
      queueMicrotask(callback);
      return retryDelays.length;
    },
  };

  const context = vm.createContext({
    EventSource,
    FormData: class MockFormData {},
    URL,
    document,
    fetch,
    navigator: { language: "ja", languages: ["ja"] },
    window,
  });
  vm.runInContext(appSource, context);
  document.dispatch("DOMContentLoaded");
  const validStoredJob = typeof storedJobID === "string" && /^job-[0-9a-f]{32}$/.test(storedJobID);
  await waitFor(
    () => configCallCount > 0 && (
      configStatus !== 200 ||
      EventSource.instances.length > 0 ||
      modelCallCount > 0 && (!validStoredJob || outputCallCount >= outputStatuses.length)
    ),
    "application startup did not finish",
  );

  return {
    document,
    EventSource,
    fetchCalls,
    get outputCallCount() {
      return outputCallCount;
    },
    retryDelays,
    storage,
  };
}

test("ordinary API 401 clears stored authentication and job state", async () => {
  const app = await launchApp({ configStatus: 401 });
  assert.equal(app.storage.getItem(authStorageKey), null);
  assert.equal(app.storage.getItem(activeJobStorageKey), null);
  assert.equal(app.EventSource.instances.length, 0);
  assert.equal(app.document.getElementById("error-panel").hidden, false);
});

test("an invalid stored active-job ID is removed without an API lookup", async () => {
  const app = await launchApp({ outputStatuses: [], storedJobID: "job-invalid" });
  assert.equal(app.storage.getItem(activeJobStorageKey), null);
  assert.equal(app.outputCallCount, 0);
  assert.equal(app.EventSource.instances.length, 0);
});

test("initial active-job lookup 404 removes the stale ID", async () => {
  const app = await launchApp({ outputStatuses: [404] });
  assert.equal(app.storage.getItem(activeJobStorageKey), null);
  assert.equal(app.storage.getItem(authStorageKey), token);
  assert.equal(app.outputCallCount, 1);
  assert.equal(app.EventSource.instances.length, 0);
});

test("active-job restore recovers from transient lookup failures", async () => {
  const app = await launchApp({ outputStatuses: [503, 429, 200] });
  assert.equal(app.outputCallCount, 3);
  assert.deepEqual(app.retryDelays, [250, 500]);
  assert.equal(app.EventSource.instances.length, 1);
  assert.equal(app.storage.getItem(activeJobStorageKey), jobID);
});

test("active-job restore stops after the bounded retry limit", async () => {
  const app = await launchApp({ outputStatuses: [503, 503, 503] });
  assert.equal(app.outputCallCount, 3);
  assert.deepEqual(app.retryDelays, [250, 500]);
  assert.equal(app.EventSource.instances.length, 0);
  assert.equal(app.storage.getItem(activeJobStorageKey), jobID);
});

test("job SSE 404 after lookup removes the stale active job", async () => {
  const app = await launchApp({ outputStatuses: [200, 404] });
  const source = app.EventSource.instances[0];
  source.emit("error");
  source.emit("error");
  await waitFor(() => app.outputCallCount === 2 && source.closed, "job SSE 404 was not handled");

  assert.equal(app.outputCallCount, 2, "concurrent SSE errors must share one probe");
  assert.equal(app.storage.getItem(activeJobStorageKey), null);
  assert.equal(app.storage.getItem(authStorageKey), token);
  assert.equal(app.document.getElementById("cancel-button").hidden, true);
});

test("terminal job SSE 401 clears session state", async () => {
  const app = await launchApp({ jobStatus: "done", outputStatuses: [200, 401] });
  const source = app.EventSource.instances[0];
  assert.ok(source, "terminal restore must open SSE to obtain the full snapshot");
  source.emit("error");
  await waitFor(() => app.outputCallCount === 2 && source.closed, "terminal job SSE 401 was not handled");

  assert.equal(app.storage.getItem(activeJobStorageKey), null);
  assert.equal(app.storage.getItem(authStorageKey), null);
  assert.equal(app.document.getElementById("error-panel").hidden, false);
});

test("terminal SSE snapshot renders outputs without another outputs fetch", async () => {
  const app = await launchApp({ jobStatus: "done" });
  const source = app.EventSource.instances[0];
  source.emit("snapshot", {
    data: JSON.stringify({ id: jobID, logs: [], outputs: ["transcript.txt"], progress: 100, status: "done" }),
  });
  await waitFor(
    () => source.closed && app.document.getElementById("download-list").children.length === 1,
    "terminal snapshot outputs were not rendered",
  );
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(app.outputCallCount, 1);
  assert.equal(app.document.getElementById("download-panel").hidden, false);
});

test("API recovery requests keep authentication in the header", async () => {
  const app = await launchApp({ outputStatuses: [200] });
  const apiCalls = app.fetchCalls.filter(({ path }) => path.startsWith("/api/"));
  assert.ok(apiCalls.length >= 3);
  for (const { options, path } of apiCalls) {
    assert.equal(options.headers["X-Auth-Token"], token, path);
    assert.equal(path.includes("?token="), false, path);
  }
  assert.equal(
    app.EventSource.instances[0].url,
    `/api/jobs/${jobID}/events?token=${token}`,
    "only EventSource authentication uses the query token",
  );
});
