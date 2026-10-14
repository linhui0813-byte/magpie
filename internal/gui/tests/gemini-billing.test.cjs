const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_GEMINI_ASSETS || path.resolve(__dirname, "../assets");
const google = {
  id: "google", name: "Google Gemini", preset: "google", icon: "gemini-color",
  chat: "https://generativelanguage.googleapis.com/v1beta/openai", responses: "", anthropic: "",
  models: [{ id: "gemini-2.5-flash", name: "Gemini 2.5 Flash", on: true }],
  chosen: [], headers: {}, fallback: [], agents: [], keyList: [],
  key: { set: true, masked: "AIza…test" }, balanceToken: { takes: false, set: false },
  geminiBillingSupported: true,
};
const config = {
  table: "billing-project.billing.gcp_billing_export_v1_AAAAAA_BBBBBB_CCCCCC",
  location: "US", billingAccount: "AAAAAA-BBBBBB-CCCCCC", serviceID: "ABCD-1234-5678",
  currency: "HKD", since: "2026-10-12", credits: 160,
};
const estimate = { exportedAt: "2026-10-14T05:00:00Z", latestUsage: "2026-10-13T23:00:00Z" };
const words = {
  en: { on: "Estimate Gemini credits from billing data", save: "Save", check: "Check estimate", estimated: "Estimated balance", setup: "Setup required: configure this provider's Credit estimate", latest: "Latest exported usage:" },
  zh: { on: "根据账单数据估算 Gemini 余额", save: "保存", check: "检查估算", estimated: "估算余额", setup: "需要设置：请配置此供应商的余额估算", latest: "最新导出的用量：" },
  ja: { on: "請求データから Gemini の残高を推定", save: "保存", check: "推定を確認", estimated: "推定残高", setup: "設定が必要です。このプロバイダーの残高の推定を設定してください", latest: "エクスポート済みの最新利用：" },
  de: { on: "Gemini-Guthaben aus Abrechnungsdaten schätzen", save: "Speichern", check: "Schätzung prüfen", estimated: "Geschätztes Guthaben", setup: "Einrichtung erforderlich: Guthabenschätzung dieses Anbieters konfigurieren", latest: "Neueste exportierte Nutzung:" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) for (const width of [1000, 440]) {
    test(`${engine} ${lang} ${width}: Gemini billing estimate setup, save and Usage`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", e => errors.push(e.message));
      let saved = null;
      const providers = () => ({ providers: [{ ...google, preset: width === 440 ? "" : google.preset, geminiBilling: saved }], presets: [{ ...google, kind: "vendor", added: true }], excluded: [], gateway: { running: true, window: true } });
      await page.route("**/*", async route => {
        const url = new URL(route.request().url());
        const json = data => route.fulfill({ json: data });
        if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
        if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
        if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
        if (url.pathname === "/api/providers") return json(providers());
        if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
        if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, cost: 0, unpriced: 0, agents: [], models: [], series: [], bucket: "day" });
        if (url.pathname === "/api/provider/balance") {
          posts.push(route.request().postDataJSON());
          return json({ ok: true, amount: "≈ HK$149.65", billingEstimate: estimate });
        }
        if (url.pathname === "/api/provider/save") {
          const body = route.request().postDataJSON(); posts.push(body);
          saved = body.geminiBilling?.table ? body.geminiBilling : null;
          return json(providers());
        }
        if (url.pathname === "/api/usage/quotas") return json([{ provider: google.id, name: google.name, icon: google.icon, windows: [], ...(saved ? { balance: "≈ HK$149.65", billingEstimate: estimate, readAt: new Date().toISOString() } : { error: "Gemini billing estimate needs setup; configure the BigQuery billing export in this provider's Credit estimate field" }) }]);
        if (url.pathname.startsWith("/api/")) return json({});
        const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
        const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)];
        try { return await route.fulfill({ body: await fs.readFile(file), contentType }); }
        catch { return route.fulfill({ status: 404, body: "" }); }
      });
      const edit = async () => {
        await page.locator(".row.provider", { hasText: google.name }).click();
        await page.getByText(words[lang].on, { exact: true }).waitFor();
      };
      const save = async () => {
        await page.locator(".editor .bar").getByRole("button", { name: words[lang].save, exact: true }).click();
        await page.waitForFunction(() => !document.querySelector(".editor"));
      };
      await page.goto("http://magpie.test/?view=usage");
      await page.getByText(words[lang].setup, { exact: true }).waitFor();
      assert.equal(await page.locator(".quota-balance > b").count(), 0, "missing export must not pretend to be zero");
      await page.goto("http://magpie.test/?view=providers");
      await edit();
      await page.getByText(words[lang].on, { exact: true }).click();
      for (const [key, value] of Object.entries(config)) await page.locator(`[data-billing="${key}"]`).fill(String(value));
      await page.getByRole("button", { name: words[lang].check, exact: true }).click();
      await page.locator(".gemini-billing .bal-res.ok").waitFor();
      assert.match(await page.locator(".gemini-billing .bal-res").textContent(), /≈ HK\$149\.65/);
      assert.deepEqual(posts.at(-1), { id: google.id, geminiBilling: config }, "checking needs no model key or billing secrets");
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "billing setup must fit a narrow screen");
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.evaluate(async () => {
          const animations = document.querySelector(".modal").getAnimations({ subtree: true });
          await Promise.all(animations.filter(a => a.effect.getComputedTiming().iterations !== Infinity).map(a => a.finished.catch(() => {})));
        });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-gemini-setup.png`) });
      }
      await save();
      assert.deepEqual(posts.at(-1).geminiBilling, config);
      assert.equal(posts.at(-1).key, "", "estimate setup leaves the saved model key alone");
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card", { hasText: google.name });
      await card.getByText(words[lang].estimated, { exact: true }).waitFor();
      assert.equal(await card.locator(".quota-balance > b").textContent(), "≈ HK$149.65");
      assert.equal(await card.getByText(words[lang].latest, { exact: false }).count(), 1);
      assert.equal(await card.locator(".quota-track").count(), 0);
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "estimate must fit a narrow screen");
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-gemini-estimate.png`) });
      }
      await page.goto("http://magpie.test/?view=providers");
      await edit();
      assert.equal(await page.locator('[data-billing="credits"]').inputValue(), "160");
      await save();
      assert.deepEqual(posts.at(-1).geminiBilling, config, "unchanged saves keep the estimate");
      await edit();
      await page.getByText(words[lang].on, { exact: true }).click();
      await save();
      assert.deepEqual(posts.at(-1).geminiBilling, {}, "turning it off explicitly clears setup");
      assert.deepEqual(errors, []);
      assert.equal(await page.locator("select").count(), 0);
    });
  }
}
