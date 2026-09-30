// A plugin as OpenCode's are written, signing in to a made-up provider
// whose requests go to $FAKE_BASE.
export const FakePlugin = async ({ client }) => ({
  config: async (cfg) => {
    cfg.provider = cfg.provider ?? {}
    cfg.provider.fakeco = {
      name: "FakeCo",
      npm: "@ai-sdk/openai-compatible",
      api: "https://fake.invalid/v1",
      models: {
        "fake-1": { name: "Fake One", limit: { context: 1000, output: 100 } },
        "fake-claude": { name: "Fake Claude", provider: { npm: "@ai-sdk/anthropic" }, limit: { context: 2000, output: 200 } },
        "fake-gemini": { name: "Fake Gemini", provider: { npm: "@ai-sdk/google" }, reasoning: true, limit: { context: 3000, output: 300 } },
      },
    }
  },
  auth: {
    provider: "fakeco",
    methods: [
      { type: "api", label: "API key" },
      {
        type: "oauth",
        label: "Browser",
        prompts: [
          { type: "select", key: "where", message: "Where?", options: [{ label: "Home", value: "home" }, { label: "Work", value: "work" }] },
          { type: "text", key: "team", message: "Team?", when: { key: "where", op: "eq", value: "work" }, validate: (v) => (v ? undefined : "Required") },
        ],
        authorize: async (inputs) => ({
          url: "https://fake.invalid/auth?where=" + inputs.where,
          instructions: "Paste the code",
          method: "code",
          callback: async (code) =>
            code === "good"
              ? { type: "success", refresh: "r-" + (inputs.team ?? "none"), access: "stale", expires: 0, accountId: (inputs.team ?? "me") + "@fake" }
              : { type: "failed" },
        }),
      },
    ],
    loader: async (getAuth, provider) => ({
      apiKey: "dummy",
      baseURL: process.env.FAKE_BASE,
      async fetch(url, init) {
        let a = await getAuth()
        if (a.type === "oauth" && a.expires < Date.now()) {
          a = { ...a, access: "fresh-" + a.refresh, expires: Date.now() + 3600e3 }
          await client.auth.set({ path: { id: "fakeco" }, body: a })
        }
        const h = new Headers(init.headers)
        h.set("authorization", "Bearer " + (a.type === "oauth" ? a.access : a.key))
        h.set("x-models", Object.keys(provider.models).sort().join(","))
        return fetch(url, { ...init, headers: h })
      },
    }),
    // magpie's: the account's allowance; "full@fake" has used its five
    // hours, which count only fake-claude
    usage: async (getAuth) => {
      const a = await getAuth()
      if (a.type !== "oauth") return { error: "an API key has no plan" }
      const full = a.accountId === "full@fake"
      return {
        plan: "Fake Pro",
        user: full ? "Full@Fake.example" : undefined,
        until: "2030-01-02T03:04:05Z",
        renew: "auto",
        resets: full ? { count: 3, byWindow: true, fiveHour: 2, weekly: 1 } : undefined,
        windows: [
          { name: "5 hours", used: full ? 100 : 25, resetsAt: Date.now() + 3600e3, span: 5 * 3600, models: ["fake-claude"] },
          { name: "Week", used: 10, resetsAt: Math.floor(Date.now() / 1000) + 86400, span: 7 * 86400 },
          { name: "Extra", used: 250, display: "$2.50", aside: true },
        ],
      }
    },
  },
  // the models an account has: refused for a dead one; a "rot-" sign-in
  // spends its refresh token asking, as a rotating one does
  provider: {
    id: "fakeco",
    models: async (p, { auth }) => {
      if (auth?.refresh === "r-dead" || auth?.key === "dead") throw new Error("the vendor refused the sign-in")
      if (auth?.type === "oauth" && auth.refresh?.startsWith("rot-")) {
        await client.auth.set({ path: { id: "fakeco" }, body: { ...auth, refresh: auth.refresh + "+" } })
      }
      if (auth?.key === "few") return { "fake-1": p.models["fake-1"] }
      return p.models
    },
  },
  "chat.headers": async (input, output) => {
    if (input.model.providerID === "fakeco") output.headers["x-plugin-model"] = input.model.id
  },
})
