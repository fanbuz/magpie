// Run with Node's test runner and Playwright on the module path; see README.md.
// A Claude account's usage-limit resets (a Max subscriber on Discord,
// holding Opus 5.5's launch reset): the Usage page's card and the menu bar
// panel's say how many and until when, as a Codex account's do; "Auto-use"
// posts settings/claude-auto-reset for that account; "Use a reset" asks
// first — a Claude reset, the one Anthropic names next — then posts
// usage/claude-reset and says what came of it, Anthropic's own answers
// in words. The Routing page's story names a Claude reset used by itself
// as Claude's. No click moves the page; no left-border accent. English and
// Chinese, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const later = new Date(Date.now() + 2 * 864e5).toISOString();
const ends = new Date(Date.now() + 22 * 864e5).toISOString();
const quotas = [
  { provider: "claude", name: "Claude Code", icon: "claude-color", user: "Me@example.com", plan: "Max 20x",
    windows: [{ name: "5 hours", used: 84, resetsAt: later }, { name: "7 days", used: 92, resetsAt: later }],
    resets: { count: 1, until: ends } },
];

function serve(lang, posts, outcome) {
  let auto = [];
  const settings = () => ({ theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", trayUsages: ["claude"], trayUsageEvery: 3, claudeAutoReset: auto });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") return json(settings());
    if (url.pathname === "/api/settings/claude-auto-reset" || url.pathname === "/api/settings/codex-auto-reset" || url.pathname === "/api/usage/claude-reset" || url.pathname === "/api/usage/codex-reset") {
      const body = req.postDataJSON();
      posts.push([url.pathname, body]);
      if (url.pathname === "/api/usage/claude-reset") return json(outcome.shift());
      if (url.pathname !== "/api/settings/claude-auto-reset") return json({});
      const who = body.user.toLowerCase();
      auto = auto.filter((u) => u !== who);
      if (body.on) auto.push(who);
      return json(settings());
    }
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { n: "↺ 1 reset", auto: "Auto-use", on: "Me@example.com uses a reset by itself once its week is used up",
    ask: "Use a Claude reset?", next: "The one used is the one Anthropic names next", title: "Uses the reset Anthropic names next",
    done: "Me@example.com: 3 windows started again", kept: "Me@example.com: nothing to reset — no window is used up yet, and the reset is kept" },
  zh: { n: "↺ 1 次重置", auto: "自动使用", on: "Me@example.com 的每周额度用完时会自动使用一次重置",
    ask: "用一次 Claude 重置？", next: "用掉的是 Anthropic 指定的下一次重置", title: "用掉的是 Anthropic 指定的下一次重置",
    done: "Me@example.com: 3 个额度窗口已重新开始", kept: "Me@example.com: 没有可重置的——还没有额度窗口用完，这次重置保留着" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a Claude account's resets shown, used, and used by themselves`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-claude-resets-${i}.png`) });
        }
        await browser.close();
      });
      const errors = [];
      const open = async (url, viewport, posts, outcome) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts, outcome));
        await page.goto(url);
        return page;
      };
      const wait = async (posts, n) => { for (let i = 0; i < 60 && posts.length < n; i++) await new Promise((r) => setTimeout(r, 50)); };
      const where = (page, sel) => page.locator(sel).evaluate((e) => [e.getBoundingClientRect().top, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)]);

      // the Usage page's card
      const posts = [];
      const page = await open("http://magpie.test/?view=usage", { width: 900, height: 700 }, posts,
        [{ code: "reset", windows: 3 }, { code: "not_limited" }]);
      const row = ".quota-resets";
      await page.locator(row).waitFor();
      assert.equal(await page.locator(row + " .resets-n").textContent(), w.n);
      assert(await page.locator(row + " .resets-until").count(), "until when it is good");
      const sel = row + " .auto-reset";
      assert.equal((await page.locator(sel).textContent()).trim(), w.auto);
      assert.equal(await page.locator(sel).getAttribute("aria-pressed"), "false", "off until turned on");
      const at = await where(page, sel);
      await page.locator(sel).click();
      await wait(posts, 1);
      assert.deepEqual(posts.at(-1), ["/api/settings/claude-auto-reset", { user: "Me@example.com", on: true }]);
      await page.locator(sel + '[aria-pressed="true"]').waitFor();
      assert.equal(await page.locator("#status").textContent(), w.on);
      await page.waitForTimeout(200);
      assert.deepEqual(await where(page, sel), at, "the click moved the page");

      // use one: asked first, as a Claude reset
      const use = row + " button.text:not(.auto-reset)";
      assert((await page.locator(use).getAttribute("title")).startsWith(w.title));
      await page.locator(use).click();
      const ask = page.locator(".reset-ask");
      await ask.waitFor();
      assert.equal(await ask.locator(".ehead b").textContent(), w.ask);
      assert(await ask.locator(".ehead img, .ehead svg, .ehead .icon").count(), "the dialog has Claude's icon");
      assert((await ask.textContent()).includes(w.next));
      assert.equal(posts.length, 1, "nothing spent before it is asked");
      await ask.locator("button.primary").click();
      await wait(posts, 2);
      assert.deepEqual(posts.at(-1), ["/api/usage/claude-reset", { user: "Me@example.com" }]);
      await page.waitForFunction((s) => document.querySelector("#status")?.textContent === s, w.done);
      await ask.waitFor({ state: "detached" });
      // Anthropic's answer when nothing is used up, in words
      await page.locator(use).click();
      await page.locator(".reset-ask button.primary").click();
      await wait(posts, 3);
      await page.waitForFunction((s) => document.querySelector("#status")?.textContent === s, w.kept);
      assert(!posts.some(([p]) => p.includes("codex")), "nothing went to Codex's endpoints");
      const border = await page.evaluate(() => [...document.querySelectorAll(".quota-resets, .quota-resets *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");

      // the menu bar panel's card
      const panelPosts = [];
      const panel = await open("http://magpie.test/?mode=panel", { width: 440, height: 600 }, panelPosts, []);
      await panel.locator('#ptabs [data-ptab="usage"]').click();
      const psel = ".pq-resets .pq-auto";
      await panel.locator(psel).waitFor();
      assert.equal(await panel.locator(".pq-resets .resets-n").textContent(), w.n);
      assert.equal(await panel.locator(psel).getAttribute("aria-pressed"), "false");
      await panel.locator(psel).click();
      await wait(panelPosts, 1);
      assert.deepEqual(panelPosts.at(-1), ["/api/settings/claude-auto-reset", { user: "Me@example.com", on: true }]);
      const [a, u] = await panel.evaluate(() => [...document.querySelectorAll(".pq-resets button")].map((b) => b.getBoundingClientRect().top));
      assert.equal(Math.round(a), Math.round(u), "the toggle and Use one… sit on one line");

      const missing = await page.evaluate(() => [
        "Use a Claude reset?", "Uses the reset Anthropic names next, good until {when}.", "The one used is the one Anthropic names next, good until {when}.",
        "that reset was already used", "nothing to reset — no window is used up yet, and the reset is kept",
        "a reset was used a short while ago — try again later", "the account can't use a reset", "resets can't be used right now — try again later",
        "Its week was used up, so one of {account}'s Claude resets was used by itself first.",
        "{who} answered {status}: its week is used up and nobody else could take the request, so one of {account}'s Claude resets was used by itself and the request is asked again, before any of the reply reaches {agent}.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a Claude reset used by itself is Claude's in the story`, async (t) => {
      const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
      const at = new Date(now.getTime() - 60e3).toISOString();
      const key = { id: "claude-me", provider: "claude", name: "Claude Code", kind: "account", model: "claude-sonnet-5" };
      const reset = { who: "me@example.com", text: "3 windows started again", agent: "claude" };
      const routes = [{ id: 100, seq: 100, time: at, agent: "claude", model: "claude-sonnet-5", provider: "claude", order: [key], done: true, status: 200, ms: 700,
        tries: [{ id: key.id, model: key.model, start: at, done: true, status: 429, ms: 300, fail: "quota", reset },
          { id: key.id, model: key.model, start: at, done: true, status: 200, ms: 400 }] }];
      const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", async (route) => {
        const url = new URL(route.request().url());
        const json = (data) => route.fulfill({ json: data });
        if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
        if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
        if (url.pathname === "/api/state") return json(state);
        if (url.pathname === "/api/plugins") return json({ plugins: [] });
        if (url.pathname === "/api/gateway/trace") {
          if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
          return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
        }
        if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day, requests: 1 }], routes: url.searchParams.get("day") ? routes : [] });
        if (url.pathname === "/api/groups") return json({ groups: [] });
        if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
        if (url.pathname.startsWith("/api/")) return json({});
        const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
        const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
        await route.fulfill({ body: await fs.readFile(file), contentType });
      });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-day").nth(1).click();
      await page.locator(".rt-req").first().click();
      await page.waitForTimeout(300);
      const got = await page.locator(".rt-steps li").evaluateAll((ls) => ls.map((l) => l.textContent));
      const said = lang === "zh" ? "于是自动使用了 me@example.com 的一次 Claude 重置" : "so one of me@example.com's Claude resets was used by itself";
      assert(got.some((s) => s.includes(said)), JSON.stringify(got));
      assert(!got.some((s) => s.includes("Codex")), JSON.stringify(got));
      assert.deepEqual(errors, []);
    });
  }
}
