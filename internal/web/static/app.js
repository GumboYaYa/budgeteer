// Keyboard-driven transaction tables (inbox and transaction list).
//
// The table markup comes from the server (txTable in transactions.templ).
// Rows are looked up on every key press because HTMX replaces the list when a
// filter changes.
(function () {
  "use strict";

  const picker = document.getElementById("picker");
  const help = document.getElementById("help");
  if (!picker) return; // not a page with a transaction table

  const undoStack = [];

  const table = () => document.querySelector("[data-tx-table]");
  const view = () => (table() ? table().dataset.view : "");
  const rows = () => Array.from(document.querySelectorAll("tr.tx-row"));
  const focused = () => document.querySelector("tr.tx-row.tx-focus");
  const selected = () => rows().filter((r) => r.classList.contains("tx-selected"));

  function focusRow(row) {
    rows().forEach((r) => r.classList.remove("tx-focus"));
    if (!row) return;
    row.classList.add("tx-focus");
    row.scrollIntoView({ block: "nearest" });
  }

  function move(delta) {
    const all = rows();
    if (all.length === 0) return;
    const i = all.indexOf(focused());
    const next = i < 0 ? 0 : Math.min(all.length - 1, Math.max(0, i + delta));
    focusRow(all[next]);
  }

  // The checkbox in the table head reflects the rows: checked when all are
  // selected, in between when only some are.
  function syncSelectAll() {
    const all = document.querySelector(".tx-check-all");
    if (!all) return;
    const total = rows().length;
    const count = selected().length;
    all.checked = total > 0 && count === total;
    all.indeterminate = count > 0 && count < total;
  }

  function setSelected(row, on) {
    row.classList.toggle("tx-selected", on);
    const box = row.querySelector(".tx-check");
    if (box) box.checked = on;
    syncSelectAll();
  }

  // The rows an action applies to: the selection, or else the focused row.
  function targets() {
    const sel = selected();
    if (sel.length > 0) return sel;
    const f = focused();
    return f ? [f] : [];
  }

  function toast(message) {
    const box = document.getElementById("toast");
    if (!box) return;
    document.getElementById("toast-text").textContent = message;
    box.classList.remove("hidden");
    clearTimeout(toast.timer);
    toast.timer = setTimeout(() => box.classList.add("hidden"), 5000);
  }

  function setInboxCount(response) {
    const n = response.headers.get("X-Inbox-Count");
    const badge = document.getElementById("inbox-count");
    if (n === null || !badge) return;
    badge.textContent = n;
    badge.classList.toggle("badge-primary", n !== "0");
    badge.classList.toggle("badge-ghost", n === "0");
    const total = document.getElementById("inbox-total");
    if (total) total.textContent = n;
  }

  async function post(url, targetRows, fields) {
    const body = new URLSearchParams(fields);
    targetRows.forEach((r) => body.append("ids", r.dataset.id));
    body.set("view", view());
    const response = await fetch(url, { method: "POST", body });
    if (!response.ok) {
      throw new Error((await response.text()).trim() || "Request failed (" + response.status + ")");
    }
    setInboxCount(response);
    return response;
  }

  // In the inbox, handled rows leave the list; they are kept for undo.
  function removeRows(targetRows, kind) {
    const all = rows();
    const last = all.indexOf(targetRows[targetRows.length - 1]);
    const after = all.slice(last + 1).find((r) => !targetRows.includes(r));
    const before = all.slice(0, last).reverse().find((r) => !targetRows.includes(r));

    const entry = { kind, items: [] };
    targetRows.forEach((row) => {
      setSelected(row, false);
      row.classList.remove("tx-focus");
      entry.items.push({ row, next: row.nextElementSibling });
    });
    // Detach in a second pass so every "next" still points at a live sibling
    // or at another row of the same batch.
    entry.items.forEach((item) => item.row.remove());
    undoStack.push(entry);

    focusRow(after || before || null);
    if (rows().length === 0 && table() && table().dataset.more !== undefined) {
      window.location.reload(); // fetch the next batch
    }
  }

  // In the transaction list, rows stay and are replaced by fresh markup.
  async function replaceRows(response, keepSelection) {
    const holder = document.createElement("template");
    holder.innerHTML = "<table><tbody>" + (await response.text()) + "</tbody></table>";
    holder.content.querySelectorAll("tr.tx-row").forEach((fresh) => {
      const old = document.getElementById(fresh.id);
      if (!old) return;
      if (old.classList.contains("tx-focus")) fresh.classList.add("tx-focus");
      if (keepSelection && old.classList.contains("tx-selected")) setSelected(fresh, true);
      old.replaceWith(fresh);
    });
    syncSelectAll();
  }

  async function act(url, fields, kind) {
    const targetRows = targets();
    if (targetRows.length === 0) return;
    try {
      const response = await post(url, targetRows, fields);
      if (view() === "inbox") removeRows(targetRows, kind);
      else await replaceRows(response);
    } catch (err) {
      toast(err.message);
    }
  }

  function categorize(categoryId) {
    return act("/api/categorize", { category_id: categoryId }, "category");
  }

  function toggleTransfer() {
    const targetRows = targets();
    if (targetRows.length === 0) return;
    // In the list, the first target decides the direction for all of them.
    const value = view() === "inbox" || targetRows[0].dataset.transfer === undefined ? "1" : "0";
    return act("/api/transfer", { value }, "transfer");
  }

  // Marks irregular expenses for the reserve. The rows stay in place, in the
  // inbox too, and keep their selection so they can be categorized next.
  async function toggleReserve() {
    const targetRows = targets();
    if (targetRows.length === 0) return;
    // The first target decides the direction for all of them.
    const value = targetRows[0].dataset.reserve === undefined ? "1" : "0";
    try {
      await replaceRows(await post("/api/reserve", targetRows, { value }), true);
    } catch (err) {
      toast(err.message);
    }
  }

  async function undo() {
    const entry = undoStack.pop();
    if (!entry) return;
    try {
      await post("/api/undo", entry.items.map((i) => i.row), { kind: entry.kind });
    } catch (err) {
      undoStack.push(entry);
      toast(err.message);
      return;
    }
    const body = table().querySelector("tbody");
    // Last removed first, so each row finds its former next sibling in place.
    entry.items.slice().reverse().forEach((item) => {
      const anchor = item.next && item.next.isConnected ? item.next : null;
      body.insertBefore(item.row, anchor);
    });
    focusRow(entry.items[0].row);
  }

  // --- pickers ---------------------------------------------------------------

  // A picker is a dialog with a search field and a list of entries; onChoose
  // gets the chosen entry. A list may end with a "create" entry, which stands
  // for a new entry named like the search text and shows up only when no
  // entry has that name yet.
  function makePicker(dialog, onChoose) {
    const input = dialog.querySelector("input");
    const list = dialog.querySelector("ul");
    const empty = dialog.querySelector(".picker-empty");
    const create = list.querySelector("[data-create]");
    const items = () => Array.from(list.querySelectorAll(".picker-item"));
    const visibleItems = () => items().filter((i) => !i.hidden);

    function activate(item) {
      items().forEach((i) => i.classList.remove("picker-active"));
      if (!item) return;
      item.classList.add("picker-active");
      item.scrollIntoView({ block: "nearest" });
    }

    // Every word of the query must occur in the entry's search text.
    function filter() {
      const query = input.value.trim();
      const words = query.toLowerCase().split(/\s+/).filter(Boolean);
      items().forEach((item) => {
        if (item === create) return;
        const hay = item.dataset.search.toLowerCase();
        item.hidden = !words.every((w) => hay.includes(w));
      });
      if (create) {
        const taken = items().some((i) => i !== create && (i.dataset.name || "").toLowerCase() === query.toLowerCase());
        create.hidden = query === "" || taken;
        create.dataset.name = query;
        create.querySelector("[data-create-name]").textContent = query;
      }
      const visible = visibleItems();
      if (empty) empty.classList.toggle("hidden", visible.length > 0);
      activate(visible[0]);
    }

    function open() {
      if (targets().length === 0) return;
      input.value = "";
      filter();
      dialog.showModal();
      input.focus();
    }

    function choose(item) {
      if (!item) return;
      dialog.close();
      input.blur();
      onChoose(item, list, create);
    }

    input.addEventListener("input", filter);
    input.addEventListener("keydown", (e) => {
      const visible = visibleItems();
      const i = visible.findIndex((v) => v.classList.contains("picker-active"));
      if (e.key === "ArrowDown") {
        e.preventDefault();
        activate(visible[Math.min(visible.length - 1, i + 1)]);
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        activate(visible[Math.max(0, i - 1)]);
      } else if (e.key === "Enter") {
        e.preventDefault();
        // Closing the picker here must not let the same key press reach the
        // page shortcuts, where Enter would open the category picker.
        e.stopPropagation();
        choose(visible[i]);
      }
    });
    list.addEventListener("click", (e) => choose(e.target.closest(".picker-item")));
    return { open };
  }

  const categoryPicker = makePicker(picker, (item, list) => {
    list.prepend(item); // most recently used first next time
    categorize(item.dataset.id);
  });

  // Puts the target rows into a recurring group, takes them out of theirs
  // (the entry with id 0), or creates the group typed into the search field.
  async function assignGroup(item, list, create) {
    const targetRows = targets();
    if (targetRows.length === 0) return;
    const creating = item === create;
    const name = item.dataset.name;
    try {
      const fields = creating ? { name } : { group_id: item.dataset.id };
      const response = await post("/api/recurring", targetRows, fields);
      await replaceRows(response, true);
      if (!creating) return;
      // Offer the new group next time without reloading the page.
      const entry = document.createElement("li");
      entry.className = "picker-item";
      entry.setAttribute("role", "option");
      entry.dataset.id = response.headers.get("X-Group-Id");
      entry.dataset.name = name;
      entry.dataset.search = name;
      const label = document.createElement("span");
      label.textContent = name;
      entry.append(label);
      list.prepend(entry);
    } catch (err) {
      toast(err.message);
    }
  }

  const groupDialog = document.getElementById("group-picker");
  const groupPicker = groupDialog ? makePicker(groupDialog, assignGroup) : null;

  // --- mouse and keyboard ----------------------------------------------------

  document.addEventListener("click", (e) => {
    if (e.target.classList.contains("tx-check-all")) {
      const on = e.target.checked;
      rows().forEach((r) => setSelected(r, on));
      return;
    }
    const row = e.target.closest("tr.tx-row");
    if (!row) return;
    focusRow(row);
    if (e.target.classList.contains("tx-check")) setSelected(row, e.target.checked);
  });
  document.addEventListener("dblclick", (e) => {
    if (e.target.closest("tr.tx-row")) categoryPicker.open();
  });

  document.addEventListener("keydown", (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (document.querySelector("dialog[open]")) return;
    const tag = e.target.tagName;
    if (tag === "INPUT" && (e.target.classList.contains("tx-check") || e.target.classList.contains("tx-check-all"))) {
      // the checkboxes must not swallow shortcuts
    } else if (e.target.closest("dialog:not([open])")) {
      // focus can linger on the picker's input right after it closed
    } else if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA" || tag === "BUTTON" || tag === "A") {
      return;
    }
    if (!table()) return;

    switch (e.key) {
      case "j":
      case "ArrowDown":
        move(1);
        break;
      case "k":
      case "ArrowUp":
        move(-1);
        break;
      case "c":
      case "Enter":
        categoryPicker.open();
        break;
      case "g":
        if (groupPicker) groupPicker.open();
        break;
      case "x": {
        const f = focused();
        if (f) setSelected(f, !f.classList.contains("tx-selected"));
        break;
      }
      case "t":
        toggleTransfer();
        break;
      case "r":
        toggleReserve();
        break;
      case "u":
        if (view() === "inbox") undo();
        break;
      case "Escape":
        selected().forEach((r) => setSelected(r, false));
        break;
      case "?":
        if (help) help.showModal();
        break;
      default:
        return;
    }
    e.preventDefault();
  });

  // Keep a row focused after HTMX swapped in a new result list.
  document.body.addEventListener("htmx:afterSwap", () => {
    if (!focused()) focusRow(rows()[0]);
  });
  focusRow(rows()[0]);
})();
