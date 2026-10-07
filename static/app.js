"use strict";

const $ = (id) => document.getElementById(id);
const els = {
  q: $("q"), n: $("n"), backend: $("backend"), showDebug: $("show-debug"), showRaw: $("show-raw"),
  status: $("status"), results: $("results"), debug: $("debug"), meta: $("meta"),
  resultsTable: $("results-table"), bucketsTable: $("buckets-table"),
  requestLink: $("request-link"), rawSection: $("raw-section"), raw: $("raw"),
  scoreHint: $("score-hint"), jevOnly: $("jev-only"),
};

const BACKEND_LABELS = { jev: "Jev (API)", embed: "EmbeddingGemma 2 (local)" };

const MAX_BUCKET_ROWS = 20;

let inflight = null; // AbortController of the latest request

function pct(p) {
  if (p >= 0.1) return (p * 100).toFixed(1) + "%";
  if (p >= 0.001) return (p * 100).toFixed(2) + "%";
  return p.toExponential(1);
}

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "style") node.style.cssText = v;
    else node.setAttribute(k, v);
  }
  for (const c of children) node.append(c);
  return node;
}

function setStatus(text, isError = false) {
  els.status.textContent = text;
  els.status.classList.toggle("error", isError);
}

async function search() {
  const input = els.q.value;
  const n = Math.max(1, parseInt(els.n.value, 10) || 10);

  if (inflight) inflight.abort();
  inflight = new AbortController();
  const { signal } = inflight;

  els.requestLink.href = "/api/request?q=" + encodeURIComponent(input);

  const params = new URLSearchParams({ q: input, n });
  if (els.backend.value) params.set("backend", els.backend.value);
  if (els.showRaw.checked) params.set("raw", "1");

  setStatus("searching…");
  const started = performance.now();
  try {
    const resp = await fetch("/api/search?" + params, { signal });
    const data = await resp.json();
    if (!resp.ok) throw new Error(data.error || resp.statusText);
    render(data, performance.now() - started);
  } catch (err) {
    if (err.name === "AbortError") return; // superseded by a newer keystroke
    setStatus("error: " + err.message, true);
  }
}

function render(data, roundTripMs) {
  const d = data.debug;
  if (!data.query) {
    setStatus("");
    els.results.replaceChildren();
    els.meta.replaceChildren();
    els.resultsTable.replaceChildren();
    els.bucketsTable.replaceChildren();
    els.raw.textContent = "";
    return;
  }
  setStatus(`"${data.query}" — ${Math.round(roundTripMs)} ms${d.cached ? " (cached)" : ""}`);

  const isEmbed = data.backend === "embed";
  const fmtScore = isEmbed ? (s) => s.toFixed(3) : pct;
  els.jevOnly.hidden = isEmbed;
  els.scoreHint.textContent = isEmbed
    ? "similarity = the better cosine of the query against the emoji's name and its name + keywords (see match). Variants of one emoji are collapsed into the best match."
    : "score = P(category related) × P(emoji | category).";

  els.results.replaceChildren(...data.results.map((r) => {
    const variants = r.variants?.length ? `\nvariants: ${r.variants.join(" ")}` : "";
    const tile = el("div", { class: "tile", title: `${r.name}${variants}\nclick to copy` },
      el("div", { class: "e" }, r.emoji),
      el("div", { class: "p" }, fmtScore(r.prob)),
      el("div", { class: "n" }, r.name));
    tile.addEventListener("click", () => {
      navigator.clipboard?.writeText(r.emoji);
      setStatus(`copied ${r.emoji} ${r.name}`);
    });
    return tile;
  }));

  const meta = isEmbed ? [
    ["input", data.input],
    ["query embedded", data.query],
    ["model", d.model],
    ["embed + rank", d.embed_latency_ms.toFixed(1) + " ms"],
    ["server total", d.total_latency_ms.toFixed(1) + " ms"],
    ["round trip", roundTripMs.toFixed(0) + " ms"],
    ["query tokens", d.query_tokens],
    ["dimensions", d.embed_dim],
    ["emoji scored", `${d.num_emojis} in ${d.num_groups} variant groups`],
  ] : [
    ["input", data.input],
    ["state sent", data.query],
    ["model", d.model],
    ["jev latency", d.cached ? "cached" : d.jev_latency_ms.toFixed(0) + " ms"],
    ["server total", d.total_latency_ms.toFixed(1) + " ms"],
    ["round trip", roundTripMs.toFixed(0) + " ms"],
    ["input tokens", d.usage.input_tokens],
    ["output tokens", d.usage.output_tokens],
    ["questions", d.num_questions],
    ["emoji scored", d.num_emojis],
    ["related categories", `${d.related_buckets} (P > 0.5)`],
  ];
  els.meta.replaceChildren(...meta.map(([k, v]) => el("div", {}, el("b", {}, k), String(v))));

  const top = data.results[0]?.prob || 1;
  const headers = isEmbed
    ? ["#", "", "name", "category", "similarity", "", "match", "variants", "keywords"]
    : ["#", "", "name", "category", "P(related)", "P(e|cat)", "score", ""];
  els.resultsTable.replaceChildren(
    el("tr", {}, ...headers.map((h) => el("th", {}, h))),
    ...data.results.map((r, i) => el("tr", {},
      el("td", {}, String(i + 1)),
      el("td", { style: "font-size:20px" }, r.emoji),
      el("td", {}, r.name),
      el("td", {}, r.bucket),
      ...(isEmbed ? [] : [
        el("td", { class: "num" }, pct(r.bucket_prob)),
        el("td", { class: "num" }, pct(r.cond_prob)),
      ]),
      el("td", { class: "num" }, fmtScore(r.prob)),
      el("td", { class: "bar" }, el("div", { class: "barfill", style: `width:${Math.max(0, r.prob / top) * 100}%` })),
      ...(isEmbed ? [
        el("td", {}, r.match),
        el("td", {}, (r.variants || []).join(" ")),
        el("td", {}, (r.keywords || []).join(", ")),
      ] : []),
    )),
  );
  if (isEmbed) return;

  const buckets = d.buckets.slice(0, MAX_BUCKET_ROWS);
  const topBucket = buckets[0]?.prob || 1;
  els.bucketsTable.replaceChildren(
    el("tr", {}, ...["category", "P(related)", "best emoji", "P(e|cat)", "conf.", ""].map((h) => el("th", {}, h))),
    ...buckets.map((b) => el("tr", {},
      el("td", {}, b.id),
      el("td", { class: "num" }, pct(b.prob)),
      el("td", {}, `${b.top_emoji || ""} ${b.top_name || ""}`),
      el("td", { class: "num" }, pct(b.top_prob || 0)),
      el("td", { class: "num" }, b.confidence ? b.confidence.toFixed(3) : "—"),
      el("td", { class: "bar" }, el("div", { class: "barfill", style: `width:${(b.prob / topBucket) * 100}%` })),
    )),
  );

  els.raw.textContent = d.raw ? JSON.stringify(d.raw, null, 2) : "";
}

function syncToggles() {
  els.debug.hidden = !els.showDebug.checked;
  els.rawSection.hidden = !els.showRaw.checked;
}

async function loadBackends() {
  const backends = await (await fetch("/api/backends")).json();
  els.backend.replaceChildren(...backends.map((b) => el("option", { value: b }, BACKEND_LABELS[b] || b)));
}

els.q.addEventListener("input", search);
els.backend.addEventListener("change", search);
els.n.addEventListener("input", search);
els.showRaw.addEventListener("change", () => { syncToggles(); search(); });
els.showDebug.addEventListener("change", syncToggles);
syncToggles();
loadBackends();
