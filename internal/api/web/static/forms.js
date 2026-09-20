// Prevents double-submission of forms across the app: disables the
// triggering submit button and swaps its label to a pending state once a
// form actually submits (after any confirm() dialog is accepted). Opt out
// per-form with data-no-pending.
(function () {
  "use strict";

  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!(form instanceof HTMLFormElement) || form.hasAttribute("data-no-pending")) return;

    var submitter = e.submitter || form.querySelector('button[type="submit"]:not([disabled])');
    if (!submitter) return;

    // Let the browser serialize the submitter's name/value before disabling it.
    window.setTimeout(function () {
      submitter.disabled = true;
      submitter.dataset.pendingLabel = submitter.textContent;
      submitter.textContent = "Working…";
    }, 0);
  });

  // Forms that submit but never navigate away (e.g. a canceled network
  // request or a same-page re-render) shouldn't leave buttons stuck.
  window.addEventListener("pageshow", function () {
    document.querySelectorAll("button[data-pending-label]").forEach(function (b) {
      b.disabled = false;
      b.textContent = b.dataset.pendingLabel;
      delete b.dataset.pendingLabel;
    });
  });
})();
