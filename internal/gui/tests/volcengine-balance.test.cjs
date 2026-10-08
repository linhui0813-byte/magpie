// Ark's separate billing credentials stay masked, survive an unchanged
// save, can be replaced/removed, and produce a dated CNY balance on Usage.
// The API is a fixture; no real credentials or gateway are accessed.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_ARK_ASSETS || path.resolve(__dirname, "../assets");
const ark = {
  id: "volcengine", name: "Volcengine Ark", icon: "volcengine-color", preset: "volcengine",
  chat: "https://ark.cn-beijing.volces.com/api/v3", responses: "", anthropic: "", catalog: "",
  models: [{ id: "doubao-seed-2.1-pro", name: "Doubao Seed 2.1 Pro", on: true }],
  agents: [], fallback: [], headers: {}, chosen: [], keyList: [], proxy: "",
  key: { set: true, masked: "ark-…e2d4" }, balanceToken: { takes: true, set: true, kind: "volcengine" },
};
const words = {
  en: { save: "Save", remove: "Remove", saved: "saved · paste a new one to replace it", setup: "Set billing credentials in this provider's Account balance field" },
  zh: { save: "保存", remove: "移除", saved: "已保存 · 粘贴新的即可替换", setup: "请在此供应商的账户余额字段中设置账务凭证" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) for (const width of [1000, 440]) {
    test(`${engine} ${lang} ${width}: Ark billing setup and Usage balance`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      let credentialsSet = true, setup = false;
      const providers = () => ({ providers: [{ ...ark, preset: width === 440 ? "" : ark.preset, balanceToken: { ...ark.balanceToken, set: credentialsSet } }], presets: [{ id: ark.id, name: ark.name, icon: ark.icon, kind: "vendor", chat: ark.chat, added: true }], excluded: [], gateway: { running: true, window: true } });
      await page.route("**/*", async (route) => {
        const url = new URL(route.request().url());
        const json = (data) => route.fulfill({ json: data });
        if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
        if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
        if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
        if (url.pathname === "/api/providers") return json(providers());
        if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
        if (url.pathname === "/api/usage") return json({ calls: 0, errors: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, unpriced: 0, path: "/fixture/usage.jsonl", bucket: "day", agents: [], models: [], series: [] });
        if (url.pathname === "/api/provider/save") {
          const body = route.request().postDataJSON();
          posts.push(body);
          if (body.balanceToken) credentialsSet = true;
          else if (body.clearBalanceToken) credentialsSet = false;
          return json(providers());
        }
        if (url.pathname === "/api/usage/quotas") return json([{ provider: ark.id, name: ark.name, icon: ark.icon, windows: [], ...(setup ? { error: "Volcengine account balance needs billing credentials; open this provider's Account balance field" } : { balance: "¥34.95", readAt: new Date().toISOString() }) }]);
        if (url.pathname.startsWith("/api/")) return json({});
        const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
        const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
        try { return await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { return route.fulfill({ status: 404, body: "" }); }
      });
      const edit = async () => {
        await page.locator(".row.provider", { hasText: ark.name }).click();
        const token = page.locator('.editor input[type="password"]').nth(1);
        await token.waitFor();
        return token;
      };
      const save = async () => {
        const count = posts.length;
        await page.locator(".editor .bar").getByRole("button", { name: words[lang].save, exact: true }).click();
        await page.waitForFunction((n) => !document.querySelector(".editor"), count);
        assert.equal(posts.length, count + 1);
        return posts.at(-1);
      };
      await page.goto("http://magpie.test/?view=providers");
      let input = await edit();
      assert.equal(await input.getAttribute("type"), "password");
      assert.equal(await input.inputValue(), "", "saved credentials are never returned to the form");
      assert.equal(await input.getAttribute("placeholder"), words[lang].saved);
      assert.match(await page.locator(".editor").textContent(), /QueryBalanceAcct/);
      assert.equal(await page.locator(".editor .bal-fix").count(), 0, "imported Ark must not ask for a relay Balance URL");
      assert.equal((await save()).balanceToken, undefined, "an unchanged save keeps the stored credentials");
      input = await edit();
      await input.fill(" test-access:test-secret ");
      assert.equal((await save()).balanceToken, "test-access:test-secret");
      input = await edit();
      const remove = input.locator("xpath=..").getByRole("button", { name: words[lang].remove, exact: true });
      await remove.scrollIntoViewIfNeeded();
      const before = await input.evaluate((e) => e.getBoundingClientRect().top);
      await remove.click();
      assert(Math.abs(await input.evaluate((e) => e.getBoundingClientRect().top) - before) <= 1, "removing credentials must not scroll");
      assert.match(await input.getAttribute("placeholder"), /Access Key ID:Secret Access Key/);
      const removed = await save();
      assert.equal(removed.clearBalanceToken, true);
      assert.equal(removed.balanceToken, undefined);

      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card", { hasText: ark.name });
      await card.locator(".quota-balance > b").waitFor();
      assert.equal(await card.locator(".quota-balance > b").textContent(), "¥34.95");
      assert.equal(await card.locator(".quota-read").count(), 1);
      assert.equal(await card.locator(".quota-track").count(), 0, "an account balance must not pretend to be plan allowance");
      assert.equal(await page.locator("#status.err").count(), 0, "Usage must render without an error toast");
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no narrow-layout overflow");
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-ark-balance.png`) });
      }
      setup = true;
      await page.reload();
      await card.getByText(words[lang].setup, { exact: true }).waitFor();
      assert.equal(await card.locator(".quota-balance > b").count(), 0, "missing setup must not display zero");
      assert.deepEqual(errors, []);
      assert.equal(await page.locator("select").count(), 0);
    });
  }
}
