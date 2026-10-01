// Run with Node's test runner and Playwright on the module path; see README.md.
// The add sheet overlays a long provider list without changing its geometry or
// scroll position. Closing it restores access to the same rows; nested editors
// still preserve logos. The API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const presets = ["anthropic", "openai", "deepseek", "moonshot", "xai", "openrouter", "siliconflow", "groq"]
  .map((id, i) => ({ id, name: id, icon: "openai", kind: i < 5 ? "vendor" : "relay", chat: `https://api.${id}.example.com/v1`, added: false }));
const providers = Array.from({ length: 22 }, (_, i) => ({
  id: "p" + i, name: "Provider " + i, icon: ["openai", "deepseek", "claude"][i % 3], preset: "", host: "api.example.com",
  models: [], agents: [], key: { set: true, masked: "sk-…ab12" },
}));

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets, excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Add provider overlays the list and leaves its position unchanged", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 800 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#providers .row").first().waitFor();
        await page.waitForTimeout(400);
        const at = () => page.evaluate(() => {
          const v = document.querySelector("#view-providers"), vr = v.getBoundingClientRect();
          const bar = v.querySelector(".after-list"), sheet = document.querySelector("#addSheet");
          return {
            top: v.scrollTop, end: v.scrollHeight - v.clientHeight,
            bar: vr.bottom - bar.getBoundingClientRect().bottom, away: getComputedStyle(bar).visibility === "hidden",
            sheet: sheet.hidden ? null : sheet.getBoundingClientRect().top - vr.top,
          };
        });

        const geometry = () => page.evaluate(() => {
          const v = document.querySelector("#view-providers");
          return { top: v.scrollTop, height: v.scrollHeight,
            rows: [...v.querySelectorAll("#providers .row")].map(r => r.getBoundingClientRect().y) };
        });
        const openAndClose = async () => {
          const before = await geometry();
          await page.locator("#addProvider").click();
          await page.waitForTimeout(350);
          assert.deepEqual(await geometry(), before, "opening leaves the list in place");
          await page.locator("#addSheet .row-head button").last().click();
          await page.locator("#addBackdrop").waitFor({ state: "hidden" });
          await page.waitForTimeout(350);
          assert.deepEqual(await geometry(), before, "closing leaves no empty space");
          assert(await page.locator("#addProvider").evaluate(e => e === document.activeElement));
        };

        // at the top of a long list the button is at the view's foot, over the rows
        let s = await at();
        assert(s.end > 200, "the list is longer than the view");
        assert(Math.abs(s.bar) <= 1, "the bar sits on the view's foot: " + s.bar);
        assert(!s.away);
        assert(await page.locator("#addProvider").isVisible());
        await openAndClose();
        // and at the end, where it is, not a pixel off
        await page.mouse.move(450, 400);
        for (let i = 0; i < 12; i++) await page.mouse.wheel(0, 400);
        await page.waitForTimeout(500);
        s = await at();
        assert(s.top >= s.end - 1, "scrolled to the end");
        assert(Math.abs(s.bar) <= 1, "the bar at the end: " + s.bar);

        await openAndClose();
        await openAndClose(); // repeated cycles must not accumulate blank space
        const before = await geometry();
        await page.locator("#addProvider").click();
        await page.keyboard.press("Escape");
        await page.locator("#addBackdrop").waitFor({ state: "hidden" });
        assert.deepEqual(await geometry(), before);
        await page.locator("#addProvider").click();
        await page.locator("#addBackdrop").click({ position: { x: 2, y: 2 } });
        await page.locator("#addBackdrop").waitFor({ state: "hidden" });
        assert.deepEqual(await geometry(), before);
        await page.locator("#addProvider").click();

        // a dialog opened and closed keeps every logo on the page
        await page.evaluate(() => document.querySelectorAll("#providers .ic, #addSheet .ic").forEach((e) => { e.dataset.was = "1"; }));
        const count = await page.locator("#providers .ic, #addSheet .ic").count();
        await page.locator('#addSheet .tile[data-pick="deepseek"]').click();
        await page.locator("#modal .editor").waitFor();
        const fresh = () => page.evaluate(() => [...document.querySelectorAll("#providers .ic:not([data-was]), #addSheet .ic:not([data-was])")].map((e) => (e.closest("[data-id], [data-pick]")?.outerHTML || e.parentElement.outerHTML).slice(0, 120)));
        assert.deepEqual(await fresh(), [], "no logo made afresh as it opened");
        await page.keyboard.press("Escape");
        await page.locator("#modal").waitFor({ state: "hidden" });
        assert.equal(await page.locator("#providers .ic[data-was], #addSheet .ic[data-was]").count(), count, "every logo kept as it closed");
        assert.equal(await page.locator("#providers .ic:not([data-was]), #addSheet .ic:not([data-was])").count(), 0);
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `add-overlay-${engine}-${lang}.png`) });
        }
        // A short, narrow window scrolls choices inside the overlay.
        await page.setViewportSize({ width: 460, height: 540 });
        await page.waitForTimeout(350);
        const compact = await geometry();
        const sheet = page.locator("#addSheet");
        const box = await sheet.boundingBox();
        assert(box.x >= 0 && box.y >= 0 && box.x + box.width <= 460 && box.y + box.height <= 540);
        await sheet.locator(".tiles").hover();
        await page.mouse.wheel(0, 600);
        await page.waitForTimeout(350);
        assert.deepEqual(await geometry(), compact, "only the choices scroll");
        await sheet.locator(".row-head button").last().click();
        await page.waitForTimeout(350);
        assert.deepEqual(await geometry(), compact, "compact window closes without a gap");
        assert.deepEqual(errors, []);
      });
    }
  });
}
