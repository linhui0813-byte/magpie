// Run with Node's test runner and Playwright on the module path; see README.md.
// A card's own refresh (Hu9956, #840: 在更新时间左侧加一个刷新按钮，鼠标悬停
// 在套餐卡片上时显示，可以单独刷新某个套餐的用量): left of "Updated …", or of
// why a reading failed, or under a balance, opening as the card is hovered;
// at rest the line starts where the card's others do. A click reads that card alone
// (POST /api/usage/quotas/refresh?provider=&user=), turns while it does, and
// draws what came back; no other card is asked, and the page doesn't scroll.
// English and Chinese; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const H = 3600e3, M = 60e3;
const iso = (ms) => new Date(ms).toISOString();

function fixtures(now) {
  const r5 = now + 2 * H;
  return [
    { provider: "claude", name: "Claude Code", icon: "claude-color", plan: "Max", user: "a@x.com", readAt: iso(now - 40 * M),
      windows: [{ name: "5 hours", used: 28, resetsAt: iso(r5) }] },
    { provider: "codex", name: "Codex", icon: "openai", plan: "Plus", user: "b@x.com", readAt: iso(now - 40 * M),
      windows: [{ name: "5 hours", used: 50, resetsAt: iso(r5) }] },
    { provider: "kimi", name: "Kimi", icon: "kimi-color", error: "HTTP 503", windows: [] },
    { provider: "relay", name: "Relay", icon: "openai", balance: "$2.00", readAt: iso(now - 40 * M), windows: [] },
  ];
}

function serve(lang, state) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/settings/quota-left") { settings.quotaLeft = route.request().postDataJSON().on; return json(settings); }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") { state.gets++; return json(state.quotas); }
    if (url.pathname === "/api/usage/quotas/refresh") {
      assert.equal(route.request().method(), "POST");
      state.refreshes.push(url.search);
      await state.gate;
      const q = state.quotas.find((x) => x.provider === url.searchParams.get("provider") && (x.user || "") === (url.searchParams.get("user") || ""));
      if (q?.windows?.length) { q.windows[0].used = 61; q.readAt = iso(Date.now()); }
      if (q?.error) { delete q.error; q.windows = [{ name: q.provider === "qwen-token-plan" ? "Month" : "Week", used: q.provider === "qwen-token-plan" ? 25 : 10 }]; q.readAt = iso(Date.now()); }
      return json(state.quotas);
    }
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage") return json({ calls: 0, cost: 0, days: [], agents: [], models: [], keys: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const qwenSignIn = {
  en: "Sign in to Bailian with bl auth login --console, then refresh this card",
  zh: "请运行 bl auth login --console 登录百炼，然后刷新这张卡片",
  ja: "bl auth login --console で Bailian にサインインしてから、このカードを更新してください",
  de: "Mit bl auth login --console bei Bailian anmelden, dann diese Karte aktualisieren",
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    test(`${engine} ${lang}: Qwen explains console sign-in and shows the monthly allowance after refresh`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 560, height: 600 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const state = { quotas: [{ provider: "qwen-token-plan", name: "Qwen Token Plan", icon: "qwen-color", user: "Bailian console", windows: [], error: "Qwen Token Plan needs a Bailian console sign-in; run bl auth login --console" }], gets: 0, refreshes: [], gate: Promise.resolve() };
      await page.route("**/*", serve(lang, state));
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card", { hasText: "Qwen Token Plan" });
      const error = card.locator(".subscription-error");
      await error.waitFor();
      assert.equal(await error.textContent(), qwenSignIn[lang]);
      assert.equal(await card.locator(".quota-n").count(), 0, "unknown quota is not shown as zero");
      await card.hover();
      await card.locator(".quota-refresh").click();
      await card.locator(".quota-n").waitFor();
      assert.match(await card.locator(".quota-n").textContent(), /25%/);
      assert.deepEqual(state.refreshes, ["?provider=qwen-token-plan&user=Bailian%20console"]);
      assert.equal(await card.locator(".subscription-error").count(), 0);
      await page.locator("#quotaMode button").nth(1).click();
      await page.waitForFunction(() => document.querySelector(".quota-n")?.textContent.includes("75%"));
      assert.match(await card.locator(".quota-n").textContent(), /75%/, "remaining allowance uses the vendor's share");
      assert.equal(await card.evaluate((e) => e.scrollWidth > e.clientWidth), false, "fits a narrow window");
    });
  }
}

const words = {
  en: { updated: "Updated 40 minutes ago", label: "Refresh this card", now: /^Updated (just now|\d+ seconds? ago)$/ },
  zh: { updated: "40分钟前更新", label: "刷新这张卡片", now: /^(刚刚|\d+秒前)更新$/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: a quota card is refreshed on its own`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      // short, so the page scrolls and a click that moved it would show
      const page = await (await browser.newContext({ viewport: { width: 900, height: 420 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      let open;
      const state = { quotas: fixtures(Date.now()), gets: 0, refreshes: [], gate: new Promise((r) => { open = r; }) };
      await page.route("**/*", serve(lang, state));
      await page.goto("http://magpie.test/?view=usage");

      const claude = page.locator(".subscription-card", { hasText: "Claude Code" });
      const read = claude.locator(".quota-read");
      await read.waitFor();
      assert.equal(await read.textContent(), w.updated, "the line reads as it did");
      const btn = read.locator("button.quota-refresh");
      assert.equal(await btn.getAttribute("aria-label"), w.label);
      const ago = read.locator(".quota-ago");
      const opacity = () => btn.evaluate((e) => getComputedStyle(e).opacity);

      // at rest: not there, the line starting where the account's does
      await page.mouse.move(890, 5);
      await btn.evaluate((e) => new Promise((done) => { const tick = () => getComputedStyle(e).opacity === "0" ? done() : requestAnimationFrame(tick); tick(); }));
      const user = await claude.locator(".subscription-account .user").boundingBox();
      assert.ok(Math.abs((await ago.boundingBox()).x - user.x) < 1, "the time lines up with the card at rest");

      // hovered: left of the time, on its line
      await claude.hover();
      await btn.evaluate((e) => new Promise((done) => { const tick = () => getComputedStyle(e).opacity === "1" && e.getBoundingClientRect().width >= 14 ? done() : requestAnimationFrame(tick); tick(); }));
      assert.equal(await opacity(), "1");
      const [b, when] = [await btn.boundingBox(), await ago.boundingBox()];
      assert.ok(b.width >= 14 && b.x + b.width <= when.x + 0.5 && Math.abs(b.y + b.height / 2 - (when.y + when.height / 2)) < 4, JSON.stringify({ b, when }));

      // a failed reading and a balance have one too
      const kimi = page.locator(".subscription-card", { hasText: "Kimi" });
      assert.equal(await kimi.locator(".subscription-error button.quota-refresh").count(), 1);
      assert.equal(await page.locator(".subscription-card", { hasText: "Relay" }).locator(".quota-read button.quota-refresh").count(), 1);

      // the click: that card alone, turning meanwhile, the page where it was
      await claude.scrollIntoViewIfNeeded();
      await page.evaluate(() => window.scrollBy(0, 40));
      const y = await page.evaluate(() => [scrollY, document.querySelector("#view-usage").scrollTop, document.scrollingElement.scrollTop]);
      const gets = state.gets;
      await claude.hover();
      await btn.click();
      await page.waitForFunction(() => document.querySelector(".quota-refresh.busy"));
      assert.equal(await claude.locator(".quota-refresh.busy").count(), 1, "turns while it reads");
      open();
      await claude.locator(".quota-n", { hasText: "61%" }).waitFor();
      assert.deepEqual(state.refreshes, ["?provider=claude&user=a%40x.com"]);
      assert.equal(state.gets, gets, "no other card asked for");
      assert.match(await claude.locator(".quota-read").textContent(), w.now);
      assert.equal(await claude.locator(".quota-refresh.busy").count(), 0);
      assert.match(await page.locator(".subscription-card", { hasText: "Codex" }).locator(".quota-n").textContent(), /50%/, "the others as they were");
      assert.deepEqual(await page.evaluate(() => [scrollY, document.querySelector("#view-usage").scrollTop, document.scrollingElement.scrollTop]), y, "the click didn't scroll");

      // a failed one read again from its own
      await kimi.hover();
      await kimi.locator(".quota-refresh").click();
      await kimi.locator(".quota-n").waitFor();
      assert.deepEqual(state.refreshes.slice(1), ["?provider=kimi"]);
      assert.deepEqual(errors, []);
    });
  }
}
