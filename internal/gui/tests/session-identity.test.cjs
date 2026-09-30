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
  { t:now, agent:"claude-desktop", agentName:"Claude Desktop", provider:"session-anthropic", providerName:"Anthropic", host:"historical@example.com", model:"claude-opus-5", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"codex", agentName:"Codex", provider:"session-openai", providerName:"OpenAI", session_account:"recorded@example.com", session_provider:"openai", model:"gpt-6-sol", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"codex", agentName:"Codex", provider:"session-unknown", providerName:"Local session", model:"gpt-6-astra", model_vendor:"OpenAI", session_provider:"relay", session_account:"creator@example.com", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"claude", agentName:"Claude Code", provider:"session-unknown", providerName:"Local session", model:"claude-opus-5", req:"claude-opus-5[1m]", model_vendor:"Anthropic", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"opencode", agentName:"OpenCode", provider:"relay", providerName:"My Relay", host:"relay.example", model:"gpt-6-astra", in:10, out:2, status:200, cost:0, priced:false },
  { t:now, agent:"claude", agentName:"Claude Code", provider:"session-unknown", providerName:"Local session", model:"unknown-alias", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"opencode", agentName:"OpenCode", provider:"unknown-relay", providerName:"Unknown provider", model:"gpt-6-astra", in:10, out:2, status:200, cost:0, priced:false },
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
        assert((await cells.nth(0).textContent()).includes("Anthropic · historical@example.com"));
        assert((await cells.nth(1).textContent()).startsWith("OpenAI"));
        assert(!(await cells.nth(1).textContent()).includes("@"),"session creator is not an API billing account");
        assert.equal(await cells.nth(0).locator(".src").count(),1);
        for (const i of [2,3]) {
          assert((await cells.nth(i).textContent()).startsWith(lang === "zh" ? "本地会话" : "Local session"));
          assert.equal(await cells.nth(i).locator(".src").count(),1,"model has no attribution badge");
          assert(!(await cells.nth(i).textContent()).includes(i === 2 ? "OpenAI" : "Anthropic"));
        }
        assert((await cells.nth(4).textContent()).includes("My Relay · relay.example"));
        assert.equal(await cells.nth(4).locator(".src").count(),0,"gateway route has no inference badge");
        assert((await cells.nth(5).textContent()).includes(lang === "zh" ? "本地会话" : "Local session"));
        assert((await cells.nth(6).textContent()).startsWith(lang === "zh" ? "未知供应商" : "Unknown provider"));
        assert.equal(await cells.nth(6).locator(".src").count(),0,"an unknown gateway provider is not a local session");
        await page.locator(".led-row").nth(2).click();
        const detail = page.locator(".led-detail");
        assert((await detail.textContent()).includes("relay"));
        assert((await detail.textContent()).includes(lang === "zh" ? "未记录可核实的服务渠道" : "does not record a verifiable service route"));
        assert((await detail.textContent()).includes("creator@example.com"));
        assert((await detail.textContent()).includes(lang === "zh" ? "会话账号" : "Session account"));
        assert(!(await cells.nth(2).textContent()).includes("@"),"unknown historical call has no current email");
        assert.deepEqual(errors,[]);
        await page.close();
      });
    }
  });
}
