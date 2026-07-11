(() => {
  "use strict";

  const marker = "#token=";
  const storageKey = "whisper-cpp-gui.auth-token";
  const tokenPattern = /^[0-9a-f]{64}$/;
  const hash = window.location.hash;
  let token = "";

  function clearStoredToken() {
    try {
      window.sessionStorage.removeItem(storageKey);
    } catch (_) {
      // Storage can be unavailable under restrictive browser settings.
    }
  }

  function readStoredToken() {
    try {
      const candidate = window.sessionStorage.getItem(storageKey) || "";
      if (tokenPattern.test(candidate)) {
        return candidate;
      }
      window.sessionStorage.removeItem(storageKey);
    } catch (_) {
      // Continue with memory-only authentication for this page load.
    }
    return "";
  }

  function storeToken(value) {
    try {
      window.sessionStorage.setItem(storageKey, value);
    } catch (_) {
      // The fragment token remains usable for this page load.
    }
  }

  if (hash.startsWith(marker)) {
    const candidate = hash.slice(marker.length);
    if (tokenPattern.test(candidate)) {
      token = candidate;
    }
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
    if (token) {
      storeToken(token);
    } else {
      token = readStoredToken();
    }
  } else {
    token = readStoredToken();
  }

  Object.defineProperty(window, "__WHISPER_CPP_GUI__", {
    value: Object.freeze({ token, clearStoredToken }),
    configurable: false,
    enumerable: false,
    writable: false,
  });
})();
