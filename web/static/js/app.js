// Marchi's only hand-written client script. Kept out of inline <script>
// tags and away from htmx's hx-on:/eval-based features so the page works
// under the strict script-src 'self' CSP (NFR-SC-05) without 'unsafe-eval'.

function csrfToken() {
  const match = document.cookie.match(/(?:^|;\s*)csrf_=([^;]*)/);
  return match ? decodeURIComponent(match[1]) : "";
}

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
    errorEl.textContent = evt.detail.xhr.responseText || "Unlock failed.";
  }
});

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
