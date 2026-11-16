"use strict";

const MAX_TEXT_BYTES = 100_000;
const MAX_RENDERED_TOKENS = 2_000;
const numberFormatter = new Intl.NumberFormat("en-US");
const utf8Encoder = new TextEncoder();

const EXAMPLE_TEXT = `Tokenization is how language models read text — and it doesn't take a giant LLM.

It's a deterministic byte-pair encoding (BPE) pass: split the text with a regex, then merge byte pairs using a fixed vocabulary. 🚀

function tokenize(text) {
  return text.match(PATTERN).flatMap(encode);
}`;

const state = {
  encodings: [],
  encoding: null,
  tokens: [],
  tokenIDs: "",
  seq: 0,
  controller: null,
  debounceTimer: 0,
  toastTimer: 0,
};

const els = {
  tabs: document.querySelector("#tabs"),
  workspace: document.querySelector("#token-panel"),
  description: document.querySelector("#encoding-description"),
  input: document.querySelector("#text-input"),
  clear: document.querySelector("#clear-button"),
  example: document.querySelector("#example-button"),
  status: document.querySelector("#status"),
  tokenCount: document.querySelector("#token-count"),
  charCount: document.querySelector("#char-count"),
  byteCount: document.querySelector("#byte-count"),
  tokenView: document.querySelector("#token-view"),
  showIDs: document.querySelector("#show-ids"),
  ids: document.querySelector("#token-ids"),
  copyIDs: document.querySelector("#copy-ids"),
  toast: document.querySelector("#toast"),
  errorBanner: document.querySelector("#error-banner"),
  errorText: document.querySelector("#error-text"),
  dismissError: document.querySelector("#dismiss-error"),
  footerEncoding: document.querySelector("#footer-encoding"),
  renderNote: document.querySelector("#render-note"),
};

function formatNumber(value) {
  return numberFormatter.format(value);
}

function updateCounts() {
  const text = els.input.value;
  els.charCount.textContent = formatNumber(Array.from(text).length);
  els.byteCount.textContent = formatNumber(utf8Encoder.encode(text).length);
}

function setStatus(text, kind = "idle") {
  els.status.textContent = text;
  els.status.classList.toggle("busy", kind === "busy");
  els.status.classList.toggle("error", kind === "error");
  els.tokenView.classList.toggle("loading", kind === "busy");
  els.tokenView.setAttribute("aria-busy", String(kind === "busy"));
}

function setTokenCount(value) {
  els.tokenCount.textContent = typeof value === "number" ? formatNumber(value) : value;
}

function showError(message) {
  els.errorText.textContent = message;
  els.errorBanner.hidden = false;
}

function hideError() {
  els.errorBanner.hidden = true;
}

function showToast(message) {
  els.toast.textContent = message;
  els.toast.classList.add("show");
  window.clearTimeout(state.toastTimer);
  state.toastTimer = window.setTimeout(() => {
    els.toast.classList.remove("show");
  }, 1800);
}

function renderTabs() {
  els.tabs.textContent = "";
  const preferred = state.encodings.find((encoding) => encoding.name === "o200k_base");
  const initial = preferred || state.encodings[0];

  for (const encoding of state.encodings) {
    const tab = document.createElement("button");
    tab.type = "button";
    tab.className = "tab";
    tab.dataset.name = encoding.name;
    tab.id = `tab-${encoding.name}`;
    tab.setAttribute("role", "tab");
    tab.setAttribute("aria-controls", "token-panel");
    tab.setAttribute("aria-selected", "false");
    tab.tabIndex = -1;

    const label = document.createElement("span");
    label.className = "tab-label";
    label.textContent = encoding.label;

    const code = document.createElement("span");
    code.className = "tab-code";
    code.textContent = encoding.name;

    tab.append(label, code);
    tab.addEventListener("click", () => selectEncoding(encoding.name));
    tab.addEventListener("keydown", handleTabKeydown);
    els.tabs.append(tab);
  }

  els.tabs.setAttribute("aria-busy", "false");
  selectEncoding(initial.name, { tokenize: false });
  // The footer is optional; the UI must still work if it has been removed.
  if (els.footerEncoding) {
    els.footerEncoding.textContent = `${state.encodings.length} encodings`;
  }
}

function selectEncoding(name, { tokenize = true } = {}) {
  const encoding = state.encodings.find((item) => item.name === name);
  if (!encoding) return;

  state.encoding = encoding.name;
  els.workspace.setAttribute("aria-labelledby", `tab-${encoding.name}`);
  for (const tab of els.tabs.querySelectorAll(".tab")) {
    const active = tab.dataset.name === name;
    tab.classList.toggle("active", active);
    tab.setAttribute("aria-selected", String(active));
    tab.tabIndex = active ? 0 : -1;
  }

  els.description.textContent =
    `${encoding.name} · ${formatNumber(encoding.vocab_size)} mergeable tokens — ${encoding.description}`;

  if (tokenize) tokenizeNow();
}

function handleTabKeydown(event) {
  const tabs = Array.from(els.tabs.querySelectorAll(".tab"));
  const current = tabs.indexOf(event.currentTarget);
  if (current === -1) return;

  let next = null;
  if (event.key === "ArrowRight") next = (current + 1) % tabs.length;
  else if (event.key === "ArrowLeft") next = (current - 1 + tabs.length) % tabs.length;
  else if (event.key === "Home") next = 0;
  else if (event.key === "End") next = tabs.length - 1;
  else return;

  event.preventDefault();
  tabs[next].focus();
  selectEncoding(tabs[next].dataset.name);
  tabs[next].scrollIntoView({ block: "nearest", inline: "nearest" });
}

function scheduleTokenize() {
  window.clearTimeout(state.debounceTimer);
  state.debounceTimer = window.setTimeout(tokenizeNow, 140);
}

function cancelPending() {
  window.clearTimeout(state.debounceTimer);
  if (state.controller) {
    state.controller.abort();
    state.controller = null;
  }
  state.seq += 1;
}

async function tokenizeNow() {
  window.clearTimeout(state.debounceTimer);
  const text = els.input.value;

  if (!state.encoding) return;
  if (text === "") {
    cancelPending();
    state.tokens = [];
    state.tokenIDs = "";
    setTokenCount(0);
    renderMessage("Start typing to see how the text is split into tokens.");
    updateIDs();
    setStatus("Ready");
    hideError();
    return;
  }

  const byteLength = utf8Encoder.encode(text).length;
  if (byteLength > MAX_TEXT_BYTES) {
    cancelPending();
    state.tokens = [];
    state.tokenIDs = "";
    setTokenCount("—");
    renderMessage("This text is over the 100 KB limit. Shorten it to tokenize it.");
    updateIDs();
    setStatus("Too large", "error");
    showError(`That input is ${formatNumber(byteLength)} bytes. This demo accepts up to ${formatNumber(MAX_TEXT_BYTES)} bytes.`);
    return;
  }

  const seq = ++state.seq;
  if (state.controller) state.controller.abort();
  state.controller = new AbortController();
  setStatus("Tokenizing…", "busy");

  let response;
  try {
    response = await fetch("/api/tokenize", {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify({ text, encoding: state.encoding }),
      signal: state.controller.signal,
    });
  } catch (error) {
    if (error.name === "AbortError" || seq !== state.seq) return;
    state.controller = null;
    failTokenize("Could not reach the tokenizer service. Restart the app and try again.");
    console.error("tokenize request:", error);
    return;
  }

  try {
    if (seq !== state.seq) return;

    let payload = null;
    try {
      payload = await response.json();
    } catch {
      payload = null;
    }

    if (seq !== state.seq) return;
    if (!response.ok) {
      failTokenize(payload?.error || `Tokenizer request failed (${response.status}).`);
      return;
    }

    state.tokens = Array.isArray(payload.tokens) ? payload.tokens : [];
    state.tokenIDs = state.tokens.map((token) => token.id).join(", ");
    renderTokens(state.tokens);
    setTokenCount(payload.count ?? state.tokens.length);
    els.charCount.textContent = formatNumber(payload.characters ?? Array.from(text).length);
    els.byteCount.textContent = formatNumber(payload.bytes ?? byteLength);
    updateIDs();
    hideError();
    setStatus("Ready");
  } catch (error) {
    if (seq !== state.seq) return;
    failTokenize(error.message || "Could not tokenize this text.");
    console.error("tokenize:", error);
  } finally {
    if (seq === state.seq) state.controller = null;
  }
}

function failTokenize(message) {
  state.tokens = [];
  state.tokenIDs = "";
  setTokenCount("—");
  renderMessage("Could not tokenize this text.");
  updateIDs();
  setStatus("Error", "error");
  showError(message);
}

function renderMessage(message) {
  hideRenderNote();
  els.tokenView.textContent = "";
  const p = document.createElement("p");
  p.className = "empty";
  p.textContent = message;
  els.tokenView.append(p);
}

function renderTokens(tokens) {
  if (!tokens.length) {
    renderMessage("No tokens — the input is empty.");
    return;
  }

  const fragment = document.createDocumentFragment();
  const rendered = tokens.slice(0, MAX_RENDERED_TOKENS);
  for (const token of rendered) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "token";
    if (token.special) {
      button.classList.add("special");
    } else {
      button.classList.add(`c${Math.abs(token.id) % 12}`);
    }
    if (token.partial) button.classList.add("partial");
    button.dataset.id = String(token.id);

    const text = document.createElement("span");
    text.className = "token-text";
    text.textContent = visibleTokenText(token.text);

    const id = document.createElement("span");
    id.className = "token-id";
    id.textContent = `#${formatNumber(token.id)}`;

    button.append(text, id);
    button.title = tokenTitle(token);
    button.setAttribute(
      "aria-label",
      `Token ${token.id}, ${JSON.stringify(token.text)}, ${token.byte_length} bytes. Activate to copy the ID.`,
    );
    button.addEventListener("click", () => {
      copyText(String(token.id), `Copied token ID ${token.id}`);
    });

    fragment.append(button);
  }

  els.tokenView.textContent = "";
  els.tokenView.append(fragment);
  els.tokenView.scrollTop = 0;

  if (tokens.length > rendered.length) {
    els.renderNote.textContent =
      `Showing the first ${formatNumber(rendered.length)} of ${formatNumber(tokens.length)} tokens. The token count and "Copy all" include every token.`;
    els.renderNote.hidden = false;
  } else {
    els.renderNote.hidden = true;
  }
}

function hideRenderNote() {
  els.renderNote.hidden = true;
}

function tokenTitle(token) {
  const kind = token.special ? "special token" : `${token.byte_length} byte${token.byte_length === 1 ? "" : "s"}`;
  const preview = token.text.length > 160 ? `${token.text.slice(0, 160)}…` : token.text;
  const partial = token.partial ? " · incomplete UTF-8 fragment" : "";
  return `ID ${token.id} · ${kind}${partial}\n${JSON.stringify(preview)}`;
}

function visibleTokenText(text) {
  const characters = Array.from(text);
  const max = 40;
  let output = "";
  for (let index = 0; index < Math.min(characters.length, max); index += 1) {
    output += visibleCharacter(characters[index]);
  }
  if (characters.length > max) {
    output += `… +${formatNumber(characters.length - max)}`;
  }
  return output || "∅";
}

function visibleCharacter(character) {
  const code = character.codePointAt(0);
  if (character === " ") return "·";
  if (character === "\n") return "↵";
  if (character === "\t") return "⇥";
  if (character === "\r") return "␍";
  if (code === 0x00a0) return "⍽";
  if (code < 0x20) return String.fromCodePoint(0x2400 + code);
  if (code === 0x7f) return "␡";
  return character;
}

function updateIDs() {
  if (!state.tokens.length) {
    els.ids.textContent = "—";
    els.copyIDs.disabled = true;
    return;
  }

  const full = state.tokenIDs;
  els.ids.textContent = full.length > 20000 ? `${full.slice(0, 20000)} …` : full;
  els.copyIDs.disabled = false;
  els.copyIDs.title = `Copy all ${formatNumber(state.tokens.length)} token IDs`;
}

async function copyText(value, message) {
  let copied = false;
  try {
    await navigator.clipboard.writeText(value);
    copied = true;
  } catch {
    const textarea = document.createElement("textarea");
    textarea.value = value;
    textarea.setAttribute("readonly", "");
    textarea.className = "clipboard-helper";
    document.body.append(textarea);
    textarea.select();
    try {
      copied = document.execCommand("copy");
    } catch {
      copied = false;
    } finally {
      textarea.remove();
    }
  }
  showToast(copied ? message : "Could not copy to the clipboard.");
}

function clearAll() {
  cancelPending();
  els.input.value = "";
  state.tokens = [];
  state.tokenIDs = "";
  updateCounts();
  setTokenCount(0);
  renderMessage("Start typing to see how the text is split into tokens.");
  updateIDs();
  setStatus("Ready");
  hideError();
  els.input.focus();
}

function showExample() {
  els.input.value = EXAMPLE_TEXT;
  updateCounts();
  tokenizeNow();
  els.input.focus();
  els.input.setSelectionRange(els.input.value.length, els.input.value.length);
}

function bindEvents() {
  let composing = false;

  els.input.addEventListener("input", () => {
    updateCounts();
    if (!composing) scheduleTokenize();
  });

  els.input.addEventListener("compositionstart", () => {
    composing = true;
  });

  els.input.addEventListener("compositionend", () => {
    composing = false;
    updateCounts();
    scheduleTokenize();
  });

  els.clear.addEventListener("click", clearAll);
  els.example.addEventListener("click", showExample);
  els.dismissError.addEventListener("click", hideError);

  els.showIDs.addEventListener("change", () => {
    els.tokenView.classList.toggle("show-ids", els.showIDs.checked);
  });

  els.copyIDs.addEventListener("click", () => {
    if (state.tokenIDs) {
      copyText(state.tokenIDs, `Copied ${formatNumber(state.tokens.length)} token IDs`);
    }
  });

  // In the desktop app, open external links in the default browser instead of
  // navigating the app window away from the tokenizer.
  document.addEventListener("click", (event) => {
    const link = event.target.closest?.("a[href]");
    if (!link) return;
    let url;
    try {
      url = new URL(link.href, location.href);
    } catch {
      return;
    }
    if (url.origin === location.origin) return;
    if (typeof window.openExternal === "function") {
      event.preventDefault();
      window.openExternal(url.href);
    }
  });
}

async function boot() {
  bindEvents();
  setStatus("Loading…");
  updateCounts();

  let payload;
  try {
    // Only a failure in this block means the service is unreachable. Errors
    // while rendering the response are reported as the bugs they are.
    const response = await fetch("/api/encodings", { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error(`Could not load encodings (${response.status}).`);
    payload = await response.json();
  } catch (error) {
    setStatus("Unavailable", "error");
    els.description.textContent = "Could not load tokenizer metadata.";
    showError(
      error instanceof TypeError
        ? "Could not reach the tokenizer service. Restart the app and try again."
        : error.message,
    );
    console.error("load encodings:", error);
    return;
  }

  state.encodings = Array.isArray(payload.encodings) ? payload.encodings : [];
  try {
    if (!state.encodings.length) throw new Error("No encodings are available.");
    renderTabs();
    setStatus("Ready");
    if (els.input.value !== "") tokenizeNow();
  } catch (error) {
    setStatus("Error", "error");
    els.description.textContent = "Could not display tokenizer metadata.";
    showError(`Could not render the tokenizer interface: ${error.message}`);
    console.error("render interface:", error);
  }
}

boot();
