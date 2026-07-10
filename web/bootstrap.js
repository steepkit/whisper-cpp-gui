(() => {
  "use strict";

  const marker = "#token=";
  const hash = window.location.hash;
  let token = "";

  if (hash.startsWith(marker)) {
    const candidate = hash.slice(marker.length);
    if (/^[0-9a-f]{64}$/.test(candidate)) {
      token = candidate;
    }
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
  }

  Object.defineProperty(window, "__WHISPER_CPP_GUI__", {
    value: Object.freeze({ token }),
    configurable: false,
    enumerable: false,
    writable: false,
  });
})();

