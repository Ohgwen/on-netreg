// Directory (LDAP) autocomplete: inputs marked data-user-search fill their
// <datalist> from /users/search as the admin types. The option value is the
// username; the label shows the person's name and email.
(function () {
  "use strict";

  document.querySelectorAll("input[data-user-search]").forEach(function (input) {
    var list = document.getElementById(input.getAttribute("list"));
    if (!list) return;
    var timer = null;
    var seq = 0;

    input.addEventListener("input", function () {
      window.clearTimeout(timer);
      var q = input.value.trim();
      if (q.length < 2) return;
      timer = window.setTimeout(function () {
        var mine = ++seq;
        fetch("/users/search?q=" + encodeURIComponent(q), { credentials: "same-origin" })
          .then(function (r) { return r.ok ? r.json() : []; })
          .then(function (users) {
            if (mine !== seq) return; // a newer keystroke superseded this
            list.textContent = "";
            users.forEach(function (u) {
              var o = document.createElement("option");
              o.value = u.username;
              o.label = [u.name, u.email].filter(Boolean).join(" · ");
              list.appendChild(o);
            });
          })
          .catch(function () {});
      }, 200);
    });
  });
})();
