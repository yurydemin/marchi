// Marchi's only hand-written client script. Kept out of inline <script>
// tags and away from htmx's hx-on:/eval-based features so the page works
// under the strict script-src 'self' CSP (NFR-SC-05) without 'unsafe-eval'.

function csrfToken() {
  const match = document.cookie.match(/(?:^|;\s*)csrf_=([^;]*)/);
  return match ? decodeURIComponent(match[1]) : "";
}

// --- Theme (dark/light) --------------------------------------------------
//
// dark: utility classes (web/static/css/input.css's @custom-variant) only
// apply when .dark is present on <html> — applied here, not via
// Tailwind's automatic prefers-color-scheme media query, so a user's
// explicit choice (the nav toggle) can override the system setting.
// Runs as the very first thing in this file so the flash of the wrong
// theme is as short as possible; it can't be eliminated outright without
// an inline <script> in <head>, which the strict script-src 'self' CSP
// (NFR-SC-05) rules out.

const themeCookieName = "theme";

function getThemeCookie() {
  const match = document.cookie.match(/(?:^|;\s*)theme=([^;]*)/);
  return match ? decodeURIComponent(match[1]) : "";
}

function resolveTheme() {
  const stored = getThemeCookie();
  if (stored === "dark" || stored === "light") {
    return stored;
  }
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function applyTheme(theme) {
  document.documentElement.classList.toggle("dark", theme === "dark");
  const btn = document.getElementById("theme-toggle");
  if (btn) {
    // Labels come from the button's own data attributes (set by
    // layout.html via {{T}}) so this file never hardcodes English text.
    btn.textContent = theme === "dark" ? btn.dataset.labelLight : btn.dataset.labelDark;
  }
}

applyTheme(resolveTheme());

document.addEventListener("click", function (evt) {
  if (evt.target.id !== "theme-toggle") {
    return;
  }
  const next = document.documentElement.classList.contains("dark") ? "light" : "dark";
  document.cookie = themeCookieName + "=" + next + "; path=/; max-age=31536000; SameSite=Lax";
  applyTheme(next);
});

document.addEventListener("htmx:configRequest", function (evt) {
  evt.detail.headers["X-Csrf-Token"] = csrfToken();
});

document.addEventListener("htmx:afterRequest", function (evt) {
  const form = evt.detail.elt;
  if (!form || form.id !== "unlock-form") {
    return;
  }
  if (evt.detail.successful) {
    window.location.reload();
    return;
  }
  const errorEl = document.getElementById("unlock-error");
  if (errorEl) {
    errorEl.textContent = evt.detail.xhr.responseText || errorEl.dataset.labelFailed;
  }
});

// handleAccountsCreate's response is pure <tr> content for #accounts-tbody
// — see its own doc comment (accounts_ui.go) for why it can never also
// carry a re-rendered <form>: HTML5's table-parsing insertion mode
// foster-parents a <form> right out of a <tbody>-targeted swap,
// corrupting it. So resetting #add-account-form back to empty after a
// successful create is this listener's job instead, entirely
// client-side — no extra request, no table-parsing minefield.
document.addEventListener("htmx:afterRequest", function (evt) {
  const form = evt.detail.elt;
  if (!form || form.id !== "add-account-form" || !evt.detail.successful) {
    return;
  }
  form.reset();
});

// --- Generic /ws job-progress client -----------------------------------
//
// Shared by the Settings page's reindex button and the Archive page's
// restore flow: both trigger a background job via a plain fetch() POST
// that returns {job_id: "..."}, then want to show progress for that job
// specifically as it arrives over the process-wide /ws feed. Every
// connected client receives every event (internal/httpapi/ws.go's
// wsHub.broadcast fans out to all of them); watchJobProgress filters by
// job_id client-side and only calls back for the job it was asked about.
// One WebSocket connection is opened lazily on first use and reused for
// every subsequent job on the page.

let jobProgressSocket = null;
const jobProgressListeners = {};

function ensureJobProgressSocket() {
  if (jobProgressSocket && jobProgressSocket.readyState <= 1) {
    return jobProgressSocket;
  }
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  jobProgressSocket = new WebSocket(proto + "//" + window.location.host + "/ws");
  jobProgressSocket.addEventListener("message", function (evt) {
    let data;
    try {
      data = JSON.parse(evt.data);
    } catch (e) {
      return;
    }
    const cb = jobProgressListeners[data.job_id];
    if (cb) {
      cb(data);
    }
  });
  return jobProgressSocket;
}

// connectJobProgressSocket resolves once the shared socket is actually
// OPEN, so a caller can wait for it before triggering a job. Without this,
// a job that finishes faster than the WebSocket handshake (e.g.
// reindexing a near-empty index) broadcasts its progress/done events into
// a socket nobody's listening on yet — wsHub.broadcast (internal/httpapi/
// ws.go) is fire-and-forget, it doesn't buffer or replay for late joiners.
function connectJobProgressSocket() {
  const socket = ensureJobProgressSocket();
  if (socket.readyState === WebSocket.OPEN) {
    return Promise.resolve(socket);
  }
  return new Promise(function (resolve) {
    socket.addEventListener("open", function () {
      resolve(socket);
    }, { once: true });
  });
}

// watchJobProgress calls onEvent for every /ws message carrying jobId,
// until an event with done:true arrives, at which point the listener
// removes itself — callers don't need to unregister manually for a job
// that's expected to finish (every job type here does).
function watchJobProgress(jobId, onEvent) {
  ensureJobProgressSocket();
  jobProgressListeners[jobId] = function (ev) {
    onEvent(ev);
    if (ev.done) {
      delete jobProgressListeners[jobId];
    }
  };
}

// --- Rules page: AND/OR condition-tree builder -----------------------
//
// A rule's conditions are an arbitrarily nested tree (internal/rules.
// MaxDepth: up to 3 levels of groups), which has no natural
// x-www-form-urlencoded field-name convention the way a flat record
// does. So the tree lives entirely in the DOM (built server-side for an
// existing rule, or by cloning the <template>s below for a fresh one),
// and gets serialized to JSON into the conditions_json field right
// before an htmx request fires — see the htmx:configRequest listener at
// the bottom of this section. Every node — server-rendered ("rule-node"
// in rules.html) or client-cloned (tpl-rule-leaf/tpl-rule-group) — uses
// the same data-node/data-kind/data-role markers, so one serializer
// walks both.

function ruleBuilderMaxDepth() {
  return 3;
}

document.addEventListener("click", function (evt) {
  const addCondition = evt.target.closest('[data-action="add-condition"]');
  if (addCondition) {
    const groupEl = addCondition.closest("[data-node]");
    const childrenEl = groupEl.querySelector('[data-role="children"]');
    const tpl = document.getElementById("tpl-rule-leaf");
    const clone = tpl.content.firstElementChild.cloneNode(true);
    clone.dataset.depth = String(parseInt(groupEl.dataset.depth, 10) + 1);
    childrenEl.appendChild(clone);
    return;
  }

  const addGroup = evt.target.closest('[data-action="add-group"]');
  if (addGroup) {
    const groupEl = addGroup.closest("[data-node]");
    const depth = parseInt(groupEl.dataset.depth, 10) + 1;
    if (depth > ruleBuilderMaxDepth()) {
      return;
    }
    const childrenEl = groupEl.querySelector('[data-role="children"]');
    const tpl = document.getElementById("tpl-rule-group");
    const clone = tpl.content.firstElementChild.cloneNode(true);
    clone.dataset.depth = String(depth);
    if (depth >= ruleBuilderMaxDepth()) {
      const nestedBtn = clone.querySelector('[data-action="add-group"]');
      if (nestedBtn) {
        nestedBtn.style.display = "none";
      }
    }
    childrenEl.appendChild(clone);
    return;
  }

  const removeNode = evt.target.closest('[data-action="remove-node"]');
  if (removeNode) {
    const nodeEl = removeNode.closest("[data-node]");
    const parentNode = nodeEl && nodeEl.parentElement && nodeEl.parentElement.closest("[data-node]");
    if (nodeEl && parentNode) {
      // Root node (no [data-node] ancestor) is never removable — a
      // builder with nothing left to submit can't be serialized.
      nodeEl.remove();
    }
  }
});

// serializeRuleNode walks one node and its descendants into the same
// {op, children} / {type, value} shape domain.RuleNode's JSON tags
// produce, matching what internal/rules.Validate expects server-side.
// Each node's own controls (its select/input) are found via a plain
// (unscoped) querySelector: they always appear in the DOM before that
// node's own [data-role="children"] container (see rules.html/the
// <template>s), so document order guarantees the first match belongs to
// this node, not a descendant.
function serializeRuleNode(nodeEl) {
  if (nodeEl.dataset.kind === "group") {
    const op = nodeEl.querySelector('[data-role="group-op"]').value;
    const childrenEl = nodeEl.querySelector('[data-role="children"]');
    const children = Array.from(childrenEl.children)
      .filter(function (el) {
        return el.matches("[data-node]");
      })
      .map(serializeRuleNode);
    return { op: op, children: children };
  }
  return {
    type: nodeEl.querySelector('[data-role="cond-type"]').value,
    value: nodeEl.querySelector('[data-role="cond-value"]').value,
  };
}

document.addEventListener("htmx:configRequest", function (evt) {
  const builder = evt.detail.elt.querySelector("[data-rule-builder]");
  if (!builder) {
    return;
  }
  const root = builder.querySelector("[data-node]");
  if (!root) {
    return;
  }
  evt.detail.parameters["conditions_json"] = JSON.stringify(serializeRuleNode(root));
});

// handleRulesCreate's response is pure <tr> content for #rules-tbody —
// see its own doc comment (rules_ui.go) for why it can never also carry
// a re-rendered <form>: HTML5's table-parsing insertion mode
// foster-parents a <form> right out of a <tbody>-targeted swap,
// corrupting it. So resetting #add-rule-form back to its just-loaded
// state after a successful create is this listener's job instead,
// entirely client-side — no extra request, no table-parsing minefield.
let ruleAddFormDefaultBuilderHTML = null;

document.addEventListener("DOMContentLoaded", function () {
  const builder = document.querySelector("#add-rule-form [data-rule-builder]");
  if (builder) {
    ruleAddFormDefaultBuilderHTML = builder.innerHTML;
  }
});

document.addEventListener("htmx:afterRequest", function (evt) {
  const form = evt.detail.elt;
  if (!form || form.id !== "add-rule-form" || !evt.detail.successful) {
    return;
  }
  form.reset();
  const builder = form.querySelector("[data-rule-builder]");
  if (builder && ruleAddFormDefaultBuilderHTML !== null) {
    builder.innerHTML = ruleAddFormDefaultBuilderHTML;
  }
});

// --- Rules page: drag-and-drop priority reordering --------------------
//
// Native HTML5 drag events (no library, no eval) on each rule row
// (draggable="true", data-rule-id in rules.html). Dragging moves the row
// in the DOM live for immediate visual feedback; dropping anywhere
// commits the new order with a single PUT /rules/reorder carrying every
// row's id top-to-bottom, and the response (the same "rule-rows"
// fragment htmx routes render) replaces the table body so the committed
// order — not just the client's local guess — is what's actually shown.

let draggedRuleRow = null;

document.addEventListener("dragstart", function (evt) {
  const row = evt.target.closest("tr[data-rule-id]");
  if (!row) {
    return;
  }
  draggedRuleRow = row;
  evt.dataTransfer.effectAllowed = "move";
  evt.dataTransfer.setData("text/plain", row.dataset.ruleId);
  row.classList.add("opacity-50");
});

document.addEventListener("dragover", function (evt) {
  if (!draggedRuleRow) {
    return;
  }
  const row = evt.target.closest("tr[data-rule-id]");
  if (!row || row === draggedRuleRow) {
    return;
  }
  evt.preventDefault();
  const rect = row.getBoundingClientRect();
  const before = evt.clientY - rect.top < rect.height / 2;
  row.parentElement.insertBefore(draggedRuleRow, before ? row : row.nextSibling);
});

document.addEventListener("drop", function (evt) {
  if (draggedRuleRow) {
    evt.preventDefault();
  }
});

document.addEventListener("dragend", function () {
  const row = draggedRuleRow;
  draggedRuleRow = null;
  if (!row) {
    return;
  }
  row.classList.remove("opacity-50");
  const tbody = row.closest("tbody[data-rules-tbody]");
  if (!tbody) {
    return;
  }
  // Number(...), not the raw string dataset value: reorderRequest.IDs on
  // the server is []int64, and Go's encoding/json rejects a JSON string
  // where a number is expected rather than silently coercing it.
  const ids = Array.from(tbody.querySelectorAll("tr[data-rule-id]")).map(function (tr) {
    return Number(tr.dataset.ruleId);
  });
  fetch("/rules/reorder", {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-Csrf-Token": csrfToken() },
    body: JSON.stringify({ ids: ids }),
  })
    .then(function (res) {
      return res.text();
    })
    .then(function (html) {
      tbody.innerHTML = html;
    })
    .catch(function () {});
});

// --- Settings page: reindex trigger -------------------------------------

document.addEventListener("click", function (evt) {
  const btn = evt.target.closest('[data-action="trigger-reindex"]');
  if (!btn) {
    return;
  }
  const statusEl = document.getElementById("reindex-status");
  btn.disabled = true;
  if (statusEl) {
    statusEl.textContent = statusEl.dataset.labelStarting;
  }
  connectJobProgressSocket()
    .then(function () {
      return fetch("/api/v1/admin/reindex", {
        method: "POST",
        headers: { "X-Csrf-Token": csrfToken() },
      });
    })
    .then(function (res) {
      return res.json();
    })
    .then(function (data) {
      watchJobProgress(data.job_id, function (ev) {
        if (statusEl) {
          statusEl.textContent = ev.message;
        }
        if (ev.done) {
          btn.disabled = false;
        }
      });
    })
    .catch(function () {
      btn.disabled = false;
      if (statusEl) {
        statusEl.textContent = statusEl.dataset.labelFailed;
      }
    });
});

// --- Archive page: restore selection + trigger --------------------------
//
// Unlike the rest of Archive UI (plain GET, full-page reloads), restore is
// a fetch()+watchJobProgress trigger like Settings' reindex button — a
// restore is a background job with a job_id and WS progress, not a page
// state change a URL can capture.

function updateRestoreButtonState() {
  const btn = document.querySelector('[data-action="trigger-restore"]');
  if (!btn) {
    return;
  }
  const anySelected = document.querySelectorAll(".restore-checkbox:checked").length > 0;
  const account = document.getElementById("restore-target-account");
  const folder = document.getElementById("restore-target-folder");
  btn.disabled = !(anySelected && account && account.value && folder && folder.value);
}

document.addEventListener("change", function (evt) {
  if (evt.target.id === "restore-select-all") {
    const checked = evt.target.checked;
    document.querySelectorAll(".restore-checkbox").forEach(function (cb) {
      cb.checked = checked;
    });
    updateRestoreButtonState();
    return;
  }
  if (evt.target.classList.contains("restore-checkbox")) {
    updateRestoreButtonState();
    return;
  }
  if (evt.target.id === "restore-target-account") {
    const folderSelect = document.getElementById("restore-target-folder");
    const tpl = document.querySelector('template[data-restore-folders-for="' + evt.target.value + '"]');
    folderSelect.innerHTML = "";
    if (!tpl) {
      folderSelect.disabled = true;
      folderSelect.innerHTML = '<option value="">Select account first&hellip;</option>';
    } else {
      folderSelect.disabled = false;
      folderSelect.appendChild(tpl.content.cloneNode(true));
    }
    updateRestoreButtonState();
    return;
  }
  if (evt.target.id === "restore-target-folder") {
    updateRestoreButtonState();
  }
});

document.addEventListener("click", function (evt) {
  const btn = evt.target.closest('[data-action="trigger-restore"]');
  if (!btn) {
    return;
  }
  const emailIDs = Array.from(document.querySelectorAll(".restore-checkbox:checked")).map(function (cb) {
    return Number(cb.dataset.emailId);
  });
  const targetAccountID = Number(document.getElementById("restore-target-account").value);
  const targetFolder = document.getElementById("restore-target-folder").value;
  const statusEl = document.getElementById("restore-status");
  btn.disabled = true;
  if (statusEl) {
    statusEl.textContent = statusEl.dataset.labelStarting;
  }
  connectJobProgressSocket()
    .then(function () {
      return fetch("/api/v1/restore", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Csrf-Token": csrfToken() },
        body: JSON.stringify({
          email_ids: emailIDs,
          target_account_id: targetAccountID,
          target_folder: targetFolder,
        }),
      });
    })
    .then(function (res) {
      return res.json();
    })
    .then(function (data) {
      watchJobProgress(data.job_id, function (ev) {
        if (statusEl) {
          statusEl.textContent = ev.message;
        }
        if (ev.done) {
          updateRestoreButtonState();
        }
      });
    })
    .catch(function () {
      if (statusEl) {
        statusEl.textContent = statusEl.dataset.labelFailed;
      }
      updateRestoreButtonState();
    });
});
