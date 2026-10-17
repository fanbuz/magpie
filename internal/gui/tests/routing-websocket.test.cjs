// Native WS traces name their fixed seat. Only two observed WS legs get
// the bidirectional live treatment; old successful traces stay unknown.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { I18N } = require("./strings.cjs").strings();
const assets = path.resolve(__dirname, "../assets");
const time = new Date().toISOString();
const seat = { id: "codex", provider: "openai", name: "OpenAI", icon: "openai", who: "Codex's own sign-in", kind: "account", agent: "codex", model: "gpt-6.1-sol" };
const request = (extra = {}) => ({ id: 1, seq: 1, time, agent: "codex", session: "ws-chat", model: "gpt-6.1-sol", provider: "openai",
  transport: "websocket", upstreamTransport: "websocket", order: [seat],
  tries: [{ id: "codex", model: seat.model, start: time, done: false }], done: false, ...extra });
const words = {
  en: ["This request completed, but its routing details were not recorded.", "Nothing could take", "client"],
  zh: ["此请求已完成，但没有记录路由详情。", "没有谁能处理", "客户端"],
  "zh-TW": ["此請求已完成，但未記錄路由詳情。", "沒有誰能處理", "用戶端"],
  ja: ["このリクエストは完了しましたが、ルーティングの詳細は記録されていません。", "対応でき", "クライアント"],
  de: ["Diese Anfrage wurde abgeschlossen, aber ihre Routingdetails wurden nicht aufgezeichnet.", "konnte", "Client"],
};
function serve(lang, feed) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:${JSON.stringify(lang)},theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window={};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", fields: [] }], profiles: [], settings: { lang, theme: "dark" } });
    if (url.pathname === "/api/gateway/trace") {
      const routes = url.searchParams.has("wait") ? await new Promise((resolve) => { feed.next = resolve; }) : [feed.current];
      return json({ mine: true, now: new Date().toISOString(), seq: feed.seq || 1, totals: { requests: 1 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/gateway/session-titles") return json({ "ws-chat": "WebSocket" });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    return route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) test(`${engine} ${lang}: observed WS legs animate only while live`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await browser.newPage({ viewport: { width: 1100, height: 900 } });
    page.setDefaultTimeout(5000);
    const feed = { current: request(), seq: 1 }, errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve(lang, feed));
    t.after(async () => { feed.next?.([]); await browser.close(); });
    await page.goto("http://magpie.test/?view=routing");
    const badge = page.locator(".rt-ws-badge");
    await badge.waitFor();
    assert((await badge.getAttribute("title")).includes(words[lang][2]));
    await page.waitForFunction(() => document.querySelectorAll(".rt-wires path.ws.ws-live").length === 2);
    assert.equal(await page.locator(".rt-ws-return").count(), 2);
    assert.equal(await page.locator(".rt-accts li").count(), 1);
    assert.doesNotMatch(await page.locator(".rt-what").textContent(), /0/);
    assert.equal(await page.locator(".rt-wires path.ws").first().evaluate((e) => getComputedStyle(e).animationName), "rt-ws-out");
    assert.equal(await page.locator(".rt-ws-return").first().evaluate((e) => getComputedStyle(e).animationName), "rt-ws-back");
    for (const theme of ["dark", "light"]) for (const width of [1100, 420]) {
      await page.evaluate((theme) => { document.documentElement.dataset.theme = theme; }, theme);
      await page.setViewportSize({ width, height: 900 });
      await page.waitForFunction(() => [...document.querySelectorAll(".rt-ws-return")].every((p) => [...document.querySelectorAll("path.ws")].some((w) => w.getAttribute("d") === p.getAttribute("d"))));
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
      assert.equal(await badge.evaluate((e) => { const b = e.getBoundingClientRect(), h = e.parentElement.getBoundingClientRect(); return b.left >= h.left && b.right <= h.right && b.top >= h.top && b.bottom <= h.bottom; }), true);
    }
    if (process.env.ARTIFACT_DIR && lang === "zh") {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.setViewportSize({ width: 1100, height: 900 });
      await page.locator(".rt-stage").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-ws-live.png`), animations: "allow" });
    }
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.waitForFunction(() => !document.querySelector(".rt-ws-return"));
    assert.equal(await page.locator("path.ws").first().evaluate((e) => getComputedStyle(e).animationName), "none");
    assert.equal(await badge.isVisible(), true);
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await page.waitForFunction(() => document.querySelectorAll(".rt-ws-return").length === 2);
    const update = async (extra, newTurn = false) => {
      for (let n = 0; !feed.next && n < 200; n++) await new Promise((resolve) => setTimeout(resolve, 25));
      assert(feed.next, "the trace is waiting for its next update");
      feed.current = request({ id: feed.current.id + Number(newTurn), ...extra, seq: ++feed.seq });
      const next = feed.next;
      feed.next = null;
      next([feed.current]);
    };
    // Route.Done is authoritative even if an old record left Try.Done false.
    for (const status of [200, 502, 499]) {
      if (status !== 200) {
        await update({}, true);
        await page.waitForFunction(() => document.querySelectorAll(".rt-wires path.ws.ws-live").length === 2);
      }
      assert.equal(await page.locator(".rt-ws-return").count(), 2, "updates reuse the two return lanes");
      await update({ done: true, status, ms: 100 });
      await page.waitForFunction(() => !document.querySelector(".rt-wires path.ws-live"));
      assert.equal(await page.locator(".rt-ws-return").count(), 0);
      assert.equal(await badge.isVisible(), true);
    }
    for (const extra of [{ transport: "http", upstreamTransport: "sse" }, { transport: undefined, upstreamTransport: undefined }, { upstreamTransport: undefined }]) {
      await update({ ...extra, done: true, status: 200, tries: [{ id: "codex", model: seat.model, start: time, done: true, status: 200 }] }, true);
      await badge.waitFor({ state: "hidden" });
      await page.waitForFunction(() => !document.querySelector("path.ws, .rt-ws-return"));
      assert.equal(await page.locator("path.ws, .rt-ws-return").count(), 0);
    }
    await update({ done: true, status: 200, order: [], tries: [], transport: undefined, upstreamTransport: undefined }, true);
    await page.waitForFunction((text) => document.querySelector(".rt-mode")?.textContent === text, words[lang][0]);
    await page.waitForFunction((text) => document.querySelector(".rt-steps .why")?.textContent === text, words[lang][0]);
    assert.equal(await page.locator(".rt-steps .bad").count(), 0, "missing successful history is not a failed attempt");
    assert.doesNotMatch(await page.locator(".rt-what").textContent(), /0/);
    await update({ done: true, status: 503, order: [], tries: [], transport: undefined, upstreamTransport: undefined }, true);
    await page.waitForFunction((text) => document.querySelector(".rt-mode")?.textContent !== text, words[lang][0]);
    assert.match(await page.locator(".rt-what").textContent(), /0/);
    const refused = (I18N[lang]?.["Nothing could take {model}."] || "Nothing could take {model}.").replace("{model}", seat.model);
    await page.waitForFunction((text) => document.querySelector(".rt-steps .why")?.textContent === text, refused);
    await update({}, true);
    await page.waitForFunction(() => document.querySelectorAll(".rt-ws-return").length === 2);
    for (let n = 0; !feed.next && n < 200; n++) await new Promise((resolve) => setTimeout(resolve, 25));
    assert(feed.next);
    feed.seq = 0;
    feed.next([]);
    await badge.waitFor({ state: "hidden" });
    await page.waitForFunction(() => !document.querySelector("path.ws, .rt-ws-return"));
    assert.deepEqual(errors, []);
  });
}
