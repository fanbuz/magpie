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
              ? { type: "success", refresh: "r-" + (inputs.team ?? "none"), access: "stale", expires: 0, accountId: "me@fake" }
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
          a = { ...a, access: "fresh", expires: Date.now() + 3600e3 }
          await client.auth.set({ path: { id: "fakeco" }, body: a })
        }
        const h = new Headers(init.headers)
        h.set("authorization", "Bearer " + (a.type === "oauth" ? a.access : a.key))
        h.set("x-models", Object.keys(provider.models).sort().join(","))
        return fetch(url, { ...init, headers: h })
      },
    }),
  },
  "chat.headers": async (input, output) => {
    if (input.model.providerID === "fakeco") output.headers["x-plugin-model"] = input.model.id
  },
})
