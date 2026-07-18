// MailVault's only hand-written client script. Kept out of inline <script>
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
