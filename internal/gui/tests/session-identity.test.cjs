// Provider attribution requires route evidence. Models and session identities
// never promote unknown requests to official providers. Legacy model_vendor
// hints in fixtures are deliberately ignored. The API is a fixture.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");
const now = new Date().toISOString();
const rows = [
  { t:now, agent:"claude-desktop", agentName:"Claude Desktop", provider:"session-anthropic", providerName:"Anthropic", session_account:"historical@example.com", session_official_login:true, model:"claude-opus-5", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"codex", agentName:"Codex", provider:"session-openai", providerName:"OpenAI", session_account:"recorded@example.com", session_official_login:true, session_provider:"openai", model:"gpt-6-sol", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"codex", agentName:"Codex", provider:"session-unknown", providerName:"Local session", model:"gpt-6-astra", model_vendor:"OpenAI", session_provider:"relay", session_account:"creator@example.com", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"claude", agentName:"Claude Code", provider:"session-unknown", providerName:"Local session", model:"claude-opus-5", req:"claude-opus-5[1m]", model_vendor:"Anthropic", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"opencode", agentName:"OpenCode", provider:"relay", providerName:"My Relay", host:"relay.example", access:"api", model:"gpt-6-astra", in:10, out:2, status:200, cost:0, priced:false },
  { t:now, agent:"claude", agentName:"Claude Code", provider:"session-unknown", providerName:"Local session", model:"unknown-alias", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"opencode", agentName:"OpenCode", provider:"unknown-relay", providerName:"Unknown provider", model:"gpt-6-astra", in:10, out:2, status:200, cost:0, priced:false },
  { t:now, agent:"codex", agentName:"Codex", provider:"session-unknown", providerName:"Local session", session_provider:"custom", session_account:"reviewer@example.com", session_official_login:true, req:"codex-auto-review", model:"codex-auto-review", source:"log", in:10, out:2, status:0, cost:0.003, priced:true, pricing_model:"gpt-5.6-luna" },
  { t:now, agent:"codex", agentName:"Codex", provider:"session-unknown", providerName:"Local session", session_provider:"my-custom-route", model:"gpt-6-astra", source:"log", in:10, out:2, status:0, cost:0, priced:false },
];

for (const engine of ["chromium", "webkit"]) {
  test(engine + ": session account identities", async t => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({channel:"chromium"}));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const page = await browser.newPage({viewport:{width:1200,height:900},reducedMotion:"reduce"});
        const errors = [];
        page.on("pageerror", e => errors.push(e.message));
        await page.route("**/*", async route => {
          const url = new URL(route.request().url());
          const json = data => route.fulfill({json:data});
          if (url.pathname === "/boot.js") return route.fulfill({contentType:"text/javascript",body:`window.bootPrefs={lang:"${lang}",theme:"light",web:false};`});
          if (url.pathname === "/wails/runtime.js") return route.fulfill({contentType:"text/javascript",body:"export const Window={};"});
          if (url.pathname === "/api/state") return json({agents:[],profiles:[],settings:{lang,theme:"light"}});
          if (url.pathname === "/api/usage/requests") return json({period:"30d",rows,calls:rows.length,total:rows.length,offset:0,errors:0,input:70,output:14,cost:0,unpriced:rows.length,series:[],by:{},agents:[],providers:[]});
          if (url.pathname === "/api/usage/quotas") return json([]);
          if (url.pathname === "/api/usage") return json({calls:0,cost:0,series:[],agents:[],models:[]});
          if (url.pathname === "/api/sessions") return json({sessions:[],dirs:[]});
          if (url.pathname === "/api/sessions/stats") return json({days:[],agents:{}});
          if (url.pathname === "/api/groups") return json({groups:[],models:[]});
          if (url.pathname.startsWith("/api/")) return json({});
          const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
          await route.fulfill({body:await fs.readFile(file),contentType:{".html":"text/html",".css":"text/css",".js":"text/javascript",".svg":"image/svg+xml"}[path.extname(file)]});
        });
        await page.goto("http://magpie.test/");
        await page.locator('[data-view="usage"]').first().click();
        await page.locator("#usageTab .opt").nth(1).click();
        await page.locator(".led-row").first().waitFor();
        const cells = page.locator(".led-row .where");
        const localName = lang === "zh" ? "本地会话" : "Local session";
        for (const [i, account] of [[0,"historical@example.com"],[1,"recorded@example.com"],[2,"creator@example.com"],[7,"reviewer@example.com"]]) {
          const text = await cells.nth(i).textContent();
          const official = [0,1,7].includes(i);
          assert(text.startsWith((official ? "OFFICIAL " : "") + account));
          assert.equal(await cells.nth(i).locator(".official").count(),official ? 1 : 0);
          assert(text.includes(localName));
          assert.equal(await cells.nth(i).locator(".access").count(),0,"account login does not establish a supplier route");
          for (const inferred of ["OpenAI","Anthropic","custom","relay","官方登录","ChatGPT login"]) assert(!text.includes(inferred));
        }
        for (const i of [3,5,8]) {
          assert.equal(await cells.nth(i).textContent(),localName,"no account means only Local session, regardless of model/provider ID");
        }
        assert((await cells.nth(4).textContent()).includes("My Relay · relay.example"));
        assert.equal(await cells.nth(4).locator(".access").textContent(),"API");
        assert((await cells.nth(6).textContent()).startsWith(lang === "zh" ? "未知供应商" : "Unknown provider"));
        assert.equal(await cells.nth(6).locator(".src").count(),0,"an unknown gateway provider is not a local session");
        await page.locator(".led-row").nth(2).click();
        const detail = page.locator(".led-detail");
        assert((await detail.textContent()).includes("relay"),"raw provider ID remains available in details");
        assert((await detail.textContent()).includes("creator@example.com"));
        assert((await detail.textContent()).includes(lang === "zh" ? "不推断供应商" : "no service provider is inferred"));
        const review = page.locator(".led-row").nth(7);
        assert.equal(await review.locator("td").nth(5).textContent(),"—","an estimate must not claim a served model");
        assert.equal(await review.locator(".swap").count(),0,"price alias is not observed forwarding");
        assert((await review.locator(".price-reference").textContent()).includes("gpt-5.6-luna"));
        for (const width of [1200,440]) {
          await page.setViewportSize({width,height:900});
          const layout = await cells.nth(7).evaluate(cell => {
            const badges = [...cell.querySelectorAll(".source-badges .src")].map(n => n.getBoundingClientRect());
            const bounds = cell.getBoundingClientRect();
            return badges.every(b => b.top > bounds.top + 14 && b.left >= bounds.left && b.right <= bounds.right && b.bottom <= bounds.bottom);
          });
          assert(layout,"local-session badge stays visible below the email at " + width);
        }
        await page.setViewportSize({width:1200,height:900});
        await review.focus();
        await page.keyboard.press("Enter");
        const officialDetail = page.locator(".led-detail").last();
        assert((await officialDetail.textContent()).includes(lang === "zh" ? "官方登录" : "Official login"));
        assert((await cells.nth(7).locator(".official").getAttribute("title")).includes(lang === "zh" ? "不代表已确认这次请求" : "does not establish the route"));
        assert.deepEqual(errors,[]);
        await page.close();
      });
    }
  });
}
