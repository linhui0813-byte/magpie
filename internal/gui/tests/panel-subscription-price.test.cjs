const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_PRICE_ASSETS || path.resolve(__dirname, "../assets");
const account = "paid@example.com";
const codes = {
  en: ["Monthly subscription cost", "{price}/mo"],
  zh: ["每月订阅费用", "{price}/月"],
  "zh-TW": ["每月訂閱費用", "{price}/月"],
  ja: ["月額サブスクリプション料金", "月額 {price}"],
  de: ["Monatliche Abokosten", "{price}/Monat"],
};
const prices = {
  [`codex|${account}`]: { amount: 100, currency: "USD" },
  [`grok|${account}`]: { amount: 30, currency: "USD" },
};
const quota = (provider, name, user) => ({
  provider, name, user, windows: [{ name: "7 days", used: 27 }],
});
const cards = [quota("codex", "Codex", account), quota("grok", "Grok", account), quota("claude", "Claude", account)];

function serve(lang, { desktop = false, saves = [] } = {}) {
  const settings = { lang, theme: "light", panelSubscriptionPrices: prices };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: !desktop })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") {
      if (route.request().method() === "POST") {
        const body = route.request().postDataJSON();
        saves.push(body);
        Object.assign(settings, body);
      }
      return json(settings);
    }
    if (url.pathname === "/api/usage/quotas") return json(cards);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml" }[path.extname(file)];
    try { return await route.fulfill({ body: await fs.readFile(file), contentType }); }
    catch { return route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, [title, format]] of Object.entries(codes)) {
    test(`${engine} ${lang}: the allowances panel shows each account's monthly price`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 360, height: 640 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?mode=panel");
      await page.locator('#ptabs [data-ptab="usage"]').click();
      for (const [id, amount] of [["codex", 100], ["grok", 30]]) {
        const price = page.locator(`#panelQuota [data-card="${id}|${account}"] .pq-who > .pq-price`);
        await price.waitFor();
        const formatted = await page.evaluate((value) => new Intl.NumberFormat(intlLang(), { style: "currency", currency: "USD", minimumFractionDigits: 0 }).format(value), amount);
        assert.equal(await price.textContent(), format.replace("{price}", formatted));
        assert.equal(await price.getAttribute("title"), title);
        assert(await price.evaluate((fee) => {
          const name = fee.parentElement.querySelector(".pq-user"), who = fee.parentElement.getBoundingClientRect();
          return fee.getBoundingClientRect().left > name.getBoundingClientRect().left
            && fee.getBoundingClientRect().right <= who.right + 1;
        }), "the full price fits beside the account on a narrow panel");
      }
      assert.equal(await page.locator('#panelQuota [data-card^="claude|"] .pq-price').count(), 0);
      assert.deepEqual(await page.evaluate((q) => [
        panelQuotaCard({ ...q, user: "other@example.com" }).querySelector(".pq-price"),
        panelQuotaCard({ ...q, from: "Remote" }).querySelector(".pq-price"),
      ], cards[0]), [null, null], "another account or a remote card cannot inherit the price");
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: Settings edits and clears one account's price`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 1000, height: 740 }, reducedMotion: "reduce" });
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const saves = [];
    await page.route("**/*", serve("en", { desktop: true, saves }));
    await page.goto("http://magpie.test/");
    await page.locator("#prefs").click();
    await page.locator("#setTab-usage").click();
    const edit = async () => {
      await page.getByRole("button", { name: "Monthly subscription cost" }).click();
      await page.getByRole("menuitemradio", { name: /Grok/ }).click();
      return page.locator("#modal .editor");
    };
    let editor = await edit();
    await editor.getByLabel("Amount").fill("34.50");
    await editor.getByRole("button", { name: "Save", exact: true }).click();
    await editor.waitFor({ state: "hidden" });
    assert.deepEqual(saves.at(-1).panelSubscriptionPrices, {
      [`codex|${account}`]: { amount: 100, currency: "USD" },
      [`grok|${account}`]: { amount: 34.5, currency: "USD" },
    });
    editor = await edit();
    assert.equal(await editor.getByLabel("Amount").inputValue(), "34.5");
    await editor.getByRole("button", { name: "Clear", exact: true }).click();
    await editor.waitFor({ state: "hidden" });
    assert.deepEqual(saves.at(-1).panelSubscriptionPrices, { [`codex|${account}`]: prices[`codex|${account}`] });
  });
}
