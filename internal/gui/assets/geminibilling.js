// Gemini's estimate uses a separately configured billing export.
export function geminiBillingFields(p, draft) {
  const wrap = el("div");
  wrap.className = "gemini-billing";
  const [toggle, cb] = tick(t("Estimate Gemini credits from billing data"), !!draft.geminiBilling);
  const inner = el("div", "inner");
  inner.style.display = draft.geminiBilling ? "grid" : "none";
  let config = draft.geminiBilling || { table: "", location: "US", billingAccount: "", serviceID: "", currency: "USD", since: new Date().toISOString().slice(0, 10), credits: null };
  const add = (label, key, placeholder, type = "text", hint = "") => {
    const i = input(config[key] ?? "", placeholder, type);
    i.dataset.billing = key;
    i.oninput = () => { config[key] = key === "credits" ? (i.value === "" ? null : Number(i.value)) : i.value.trim(); };
    inner.append(...field(t(label), i, hint ? t(hint) : ""));
  };
  add("Billing export table", "table", "project.dataset.table");
  add("Dataset location", "location", "US / EU / asia-east1");
  add("Billing account ID", "billingAccount", "XXXXXX-XXXXXX-XXXXXX");
  add("Gemini service ID", "serviceID", "XXXX-XXXX-XXXX", "text", "Use the Gemini API service ID from your billing export; the estimate includes all projects sharing this account.");
  add("Billing currency", "currency", "HKD / USD");
  add("Start date (UTC)", "since", "YYYY-MM-DD", "date");
  add("Recorded funds", "credits", "160.00", "number", "Balance at 00:00 UTC on the start date, plus all later top-ups, in the billing currency. Update this amount after every top-up.");
  const buttons = el("div", "pair wrap");
  const check = el("button", "text action", t("Check estimate"));
  check.disabled = !p;
  const help = el("button", "link", t("Setup guide ↗"));
  help.onclick = () => api("open", { url: "https://github.com/linhui0813-byte/magpie/blob/main/docs/gemini-billing-estimate.md" });
  buttons.append(check, help);
  const result = el("div", "bal-res");
  check.onclick = async () => {
    check.disabled = true;
    result.className = "bal-res wait"; result.textContent = "…";
    try {
      const r = await api("provider/balance", { id: p.id, geminiBilling: { ...config } });
      result.className = "bal-res " + (r.error ? "bad" : "ok");
      result.textContent = r.error ? balanceError(r.error) || r.error : t("Estimated balance") + " " + r.amount;
    } catch (e) { result.className = "bal-res bad"; result.textContent = e.message; }
    finally { check.disabled = false; }
  };
  buttons.append(result);
  inner.append(...field(t("Estimate"), buttons, t("Uses Google Cloud credentials and a BigQuery billing export. Refreshes at most every 15 minutes; billing data may be over 24 hours late. This estimate is not the prepaid wallet balance.")));
  cb.onchange = () => { draft.geminiBilling = cb.checked ? config : null; inner.style.display = cb.checked ? "grid" : "none"; };
  wrap.append(toggle, inner);
  return field(t("Credit estimate"), wrap, "");
}
