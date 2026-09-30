// Run with Node's test runner and Playwright on the module path; see README.md.
// A built-in subscription a community plugin can run (Zed here): its
// editor says which runs it, "Move to the plugin" posts provider/move and
// then says the plugin runs it, with "Use the built-in again" posting
// provider/moveback; a failed move says why it stays built-in. The add
// sheet no longer offers the built-in Zed once it runs on its plugin. In
// English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const pkg = "@magpie-community/opencode-zed-auth";

function serve(lang, posts) {
  let move = { package: pkg, state: "failed", error: "the plugin doesn't serve claude-sonnet-5" };
  const payload = () => {
    const onPlugin = move.state === "plugin";
    const zed = {
      id: "zed", name: "Zed", icon: "zed", chat: "", responses: "", anthropic: "", catalog: "",
      models: [{ id: "claude-sonnet-5", name: "Claude Sonnet 5", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: {},
      account: { agent: onPlugin ? "zed" : "zed", agentName: "Zed", user: "ada", logins: [{ user: "ada", active: true, on: true }, { user: "bob", on: true }] },
      move,
    };
    return {
      providers: [zed], presets: [], excluded: [], gateway: { running: true, window: true },
      plugins: onPlugin ? [{ id: "zed", pid: "zed", name: "Zed", icon: "zed", spec: pkg, signedIn: true, models: 1, methods: [] }] : [],
      onPlugins: onPlugin ? ["zed"] : [],
    };
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(payload());
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/move" || url.pathname === "/api/provider/moveback") {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      move = { package: pkg, state: url.pathname.endsWith("moveback") ? "back" : "plugin" };
      return json(payload());
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { runs: "Runs on", move: "Move to the plugin", back: "Use the built-in again", failed: "The last move didn't go through, so it stays built-in: the plugin doesn't serve claude-sonnet-5", onPlugin: "The community plugin " + pkg, builtin: "magpie's built-in sign-in", subs: "Subscriptions" },
  zh: { runs: "运行方式", move: "迁移到插件", back: "改回内置", failed: "上次迁移没有成功，仍使用内置：the plugin doesn't serve claude-sonnet-5", onPlugin: "社区插件 " + pkg, builtin: "magpie 内置的登录", subs: "订阅" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a built-in subscription moves to its plugin and back`, async (t) => {
      const w = L[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Zed" }).click();
      const ed = page.locator("#modal .editor");
      const btn = ed.locator("button", { hasText: w.move });
      await btn.waitFor();
      const text = await ed.innerText();
      assert.ok(text.includes(w.runs), "no Runs on field");
      assert.ok(text.includes(w.builtin), "doesn't say it's built-in");
      assert.ok(text.includes(w.failed), "doesn't say why the last move failed");
      if (process.env.ARTIFACT_DIR) await ed.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-move-${engine}-${lang}.png`) });

      await btn.click();
      for (let i = 0; i < 50 && !posts.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(posts.map((p) => [p.path, p.body.id]), [["/api/provider/move", "zed"]]);

      // the editor again: the plugin runs it now, and the way back
      await page.locator(".row.provider", { hasText: "Zed" }).click();
      const back = ed.locator("button", { hasText: w.back });
      await back.waitFor();
      assert.ok((await ed.innerText()).includes(w.onPlugin), "doesn't name the plugin");
      if (process.env.ARTIFACT_DIR) await ed.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-moved-${engine}-${lang}.png`) });
      await page.keyboard.press("Escape");

      // the add sheet offers Zed once, as the plugin's
      await page.locator("#addProvider").click();
      const sheet = page.locator("#addSheet");
      await sheet.locator(".kind b", { hasText: w.subs }).waitFor();
      assert.equal(await sheet.locator('.tile[data-pick="Zed"]').count(), 1);
      await page.keyboard.press("Escape");

      await page.locator(".row.provider", { hasText: "Zed" }).click();
      await back.click();
      for (let i = 0; i < 50 && posts.length < 2; i++) await page.waitForTimeout(50);
      assert.deepEqual(posts.map((p) => p.path), ["/api/provider/move", "/api/provider/moveback"]);
      assert.deepEqual(errors, []);
    });
  }
}
