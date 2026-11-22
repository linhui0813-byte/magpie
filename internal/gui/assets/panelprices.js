// The tray's prices can be edited from the main window's Usage settings.
export function renderPanelPrices() {
  const button = el("button", "proto pick", t("Edit"));
  button.type = "button";
  button.setAttribute("aria-label", t("Monthly subscription cost"));
  button.setAttribute("aria-haspopup", "menu");
  button.onclick = async (event) => {
    event.stopPropagation();
    if (button.classList.contains("open")) return closeProtoMenu();
    if (!quotas) await loadQuotas();
    if (!button.isConnected) return;
    const accounts = new Map();
    for (const q of quotas || []) {
      if (q.user && !q.from && !q.balanceCard && (!q.kind || q.kind === "subscription") && !(q.balance && !q.windows?.length)) {
        accounts.set(trayCardID(q), q);
      }
    }
    // A price can still be cleared while its account is signed out.
    for (const key of Object.keys(prefs.panelSubscriptionPrices || {})) {
      if (!accounts.has(key)) {
        const sep = key.indexOf("|");
        accounts.set(key, { name: key.slice(0, sep), user: key.slice(sep + 1) });
      }
    }
    const opts = [...accounts].map(([v, q]) => ({
      v, name: q.name, icon: q.icon, note: q.alias || q.user,
    }));
    if (!opts.length) opts.push({ v: "", name: "No subscriptions yet" });
    openProtoMenu(button, opts, "", (key) => {
      if (accounts.has(key)) editPanelPrice(key);
    }, "Monthly subscription cost");
  };
  $("#panelPricePick").replaceChildren(button);
}

function editPanelPrice(key) {
  const price = prefs.panelSubscriptionPrices?.[key];
  const ed = el("div", "editor");
  const head = el("div", "ehead");
  head.append(el("b", "", t("Monthly subscription cost")));
  const amount = input(price?.amount ?? "", "", "number");
  amount.min = "0";
  amount.step = "any";
  amount.required = true;
  amount.setAttribute("aria-label", t("Amount"));
  const currency = input(price?.currency || "USD", "USD");
  currency.pattern = "[A-Za-z]{3}";
  currency.required = true;
  currency.setAttribute("aria-label", t("Currency"));
  const fields = el("div", "fields");
  fields.append(...field(t("Amount"), amount), ...field(t("Currency"), currency));

  const bar = el("div", "bar");
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelAsk;
  const clear = el("button", "text", t("Clear"));
  const save = el("button", "text primary", t("Save"));
  const update = async (value) => {
    const panelSubscriptionPrices = { ...prefs.panelSubscriptionPrices };
    if (value) panelSubscriptionPrices[key] = value;
    else delete panelSubscriptionPrices[key];
    clear.disabled = save.disabled = true;
    await savePrefs({ panelSubscriptionPrices });
    const saved = prefs.panelSubscriptionPrices?.[key];
    const matches = value ? saved?.amount === value.amount && saved.currency === value.currency : !saved;
    if (matches && confirmAsk === ed) closeConfirmAsk();
    clear.disabled = save.disabled = false;
  };
  clear.onclick = () => update(null);
  save.onclick = () => {
    if (amount.reportValidity() && currency.reportValidity()) {
      update({ amount: amount.valueAsNumber, currency: currency.value.toUpperCase() });
    }
  };
  bar.append(cancel, el("span", "grow"), ...(price ? [clear] : []), save);
  ed.append(head, fields, bar);
  confirmAsk = ed;
  openModal(ed);
  amount.focus(NO_SCROLL);
}
