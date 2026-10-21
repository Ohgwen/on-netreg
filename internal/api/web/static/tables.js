// Generic enhancer for tables marked with [data-datatable]: adds
// client-side column sorting, pagination, a show/hide-columns panel, and
// Copy/CSV export. Also wires up the devices table's select-all + bulk
// action bar, when present.
(function () {
  "use strict";

  function init(container) {
    var table = container.querySelector("table");
    if (!table) return;
    var tbody = table.tBodies[0];
    var headerRow = table.tHead.rows[0];
    var storageKey = "dt:" + (container.dataset.storageKey || table.id || "table");
    var pageSize = parseInt(container.dataset.pageSize, 10) || 20;

    var allRows = Array.prototype.filter.call(tbody.rows, function (r) {
      return !r.querySelector("td.empty");
    });
    if (allRows.length === 0) return;

    var headers = Array.prototype.slice.call(headerRow.cells);
    var sortState = { index: -1, dir: 1 };
    var page = 1;

    // Page, page size, sort and scroll position survive the round trip of
    // submitting a row's form and being redirected back, so an edit doesn't
    // dump you on page 1. Kept per tab (sessionStorage) and per URL, so
    // different filters/searches each remember their own place.
    var viewKey = storageKey + ":view:" + location.pathname + stripFlash(location.search);
    var saved = loadView();
    if (saved.pageSize === "all" || [10, 20, 50, 100, 200].indexOf(saved.pageSize) >= 0) pageSize = saved.pageSize;

    var toolbar = document.createElement("div");
    toolbar.className = "dt-toolbar";
    container.insertBefore(toolbar, container.firstChild);

    // --- Show/Hide Fields panel ---
    var keyedHeaders = headers.filter(function (h) { return h.dataset.key; });
    if (keyedHeaders.length) {
      var panel = document.createElement("details");
      panel.className = "dt-fields";
      var summary = document.createElement("summary");
      summary.textContent = "▶ Show/Hide Fields";
      panel.appendChild(summary);
      var body = document.createElement("div");
      body.className = "dt-fields-body";
      panel.appendChild(body);

      var hidden = loadHiddenCols();
      keyedHeaders.forEach(function (h) {
        var idx = headers.indexOf(h);
        var label = document.createElement("label");
        var cb = document.createElement("input");
        cb.type = "checkbox";
        cb.checked = !hidden[h.dataset.key];
        cb.addEventListener("change", function () {
          setColumnVisible(idx, cb.checked);
          hidden[h.dataset.key] = !cb.checked;
          saveHiddenCols(hidden);
        });
        label.appendChild(cb);
        label.appendChild(document.createTextNode(" " + h.textContent.trim()));
        body.appendChild(label);
        if (hidden[h.dataset.key]) setColumnVisible(idx, false);
      });
      toolbar.appendChild(panel);
    }

    // --- Export ---
    var exportBar = document.createElement("div");
    exportBar.className = "dt-export";
    var copyBtn = document.createElement("button");
    copyBtn.type = "button";
    copyBtn.textContent = "Copy";
    copyBtn.addEventListener("click", function () { doExport("tsv"); });
    var csvBtn = document.createElement("button");
    csvBtn.type = "button";
    csvBtn.textContent = "CSV";
    csvBtn.addEventListener("click", function () { doExport("csv"); });
    exportBar.appendChild(copyBtn);
    exportBar.appendChild(csvBtn);
    toolbar.appendChild(exportBar);

    function exportableCols() {
      return headers
        .map(function (h, i) { return { h: h, i: i }; })
        .filter(function (c) { return !c.h.classList.contains("no-export") && c.h.style.display !== "none"; });
    }

    function doExport(kind) {
      var cols = exportableCols();
      var lines = [cols.map(function (c) { return escapeField(c.h.textContent.trim(), kind); }).join(kind === "csv" ? "," : "\t")];
      allRows.forEach(function (r) {
        lines.push(cols.map(function (c) {
          return escapeField(r.cells[c.i] ? r.cells[c.i].textContent.trim().replace(/\s+/g, " ") : "", kind);
        }).join(kind === "csv" ? "," : "\t"));
      });
      var text = lines.join("\n");
      if (kind === "tsv") {
        if (navigator.clipboard) navigator.clipboard.writeText(text).catch(function () {});
        return;
      }
      var blob = new Blob([text], { type: "text/csv;charset=utf-8;" });
      var a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = (container.dataset.storageKey || "export") + ".csv";
      document.body.appendChild(a);
      a.click();
      a.remove();
    }

    function escapeField(v, kind) {
      if (kind === "csv" && /[",\n]/.test(v)) return '"' + v.replace(/"/g, '""') + '"';
      return v;
    }

    // --- Sorting ---
    headers.forEach(function (h, idx) {
      if (h.classList.contains("no-sort")) return;
      h.classList.add("sortable");
      h.setAttribute("tabindex", "0");
      h.setAttribute("role", "button");
      h.setAttribute("aria-sort", "none");
      var label = h.textContent.trim();
      h.setAttribute("aria-label", label + ", sort column");

      function sortBy() {
        applySort(idx, sortState.index === idx ? -sortState.dir : 1);
        page = 1;
        render();
      }

      h.addEventListener("click", sortBy);
      h.addEventListener("keydown", function (e) {
        if (e.key === "Enter" || e.key === " " || e.key === "Spacebar") {
          e.preventDefault();
          sortBy();
        }
      });
    });

    function applySort(idx, dir) {
      var h = headers[idx];
      if (!h || h.classList.contains("no-sort")) return;
      sortState.dir = dir;
      sortState.index = idx;
      headers.forEach(function (o) {
        o.classList.remove("sort-asc", "sort-desc");
        if (o.hasAttribute("aria-sort")) o.setAttribute("aria-sort", "none");
      });
      h.classList.add(sortState.dir === 1 ? "sort-asc" : "sort-desc");
      h.setAttribute("aria-sort", sortState.dir === 1 ? "ascending" : "descending");
      var type = h.dataset.sortType || "text";
      allRows.sort(function (a, b) {
        return sortState.dir * compareCells(a.cells[idx], b.cells[idx], type);
      });
    }

    function compareCells(a, b, type) {
      var av = a ? a.textContent.trim() : "";
      var bv = b ? b.textContent.trim() : "";
      if (type === "date") {
        return (Date.parse(av) || 0) - (Date.parse(bv) || 0);
      }
      if (type === "ip") {
        return ipToNum(av) - ipToNum(bv);
      }
      var an = parseFloat(av), bn = parseFloat(bv);
      if (!isNaN(an) && !isNaN(bn) && /^-?[\d.]+$/.test(av) && /^-?[\d.]+$/.test(bv)) return an - bn;
      return av.localeCompare(bv);
    }

    function ipToNum(ip) {
      var m = ip.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)/);
      if (!m) return -1;
      return ((+m[1]) << 24) + ((+m[2]) << 16) + ((+m[3]) << 8) + (+m[4]);
    }

    // --- Pagination ---
    var pager = document.createElement("div");
    pager.className = "dt-pager";
    container.appendChild(pager);

    function render() {
      var sizeVal = pageSize === "all" ? allRows.length : pageSize;
      var totalPages = Math.max(1, Math.ceil(allRows.length / sizeVal));
      if (page > totalPages) page = totalPages;
      var start = (page - 1) * sizeVal;
      var end = Math.min(start + sizeVal, allRows.length);

      // Rebuild the body in one fragment rather than row by row, so the
      // browser lays the table out once.
      var frag = document.createDocumentFragment();
      allRows.forEach(function (r, i) {
        r.style.display = i >= start && i < end ? "" : "none";
        frag.appendChild(r);
      });
      tbody.innerHTML = "";
      tbody.appendChild(frag);
      saveView();

      pager.innerHTML = "";
      var info = document.createElement("span");
      info.className = "dt-info";
      info.textContent = "Showing " + (allRows.length ? start + 1 : 0) + " to " + end + " of " + allRows.length + " entries";
      pager.appendChild(info);

      var sizeSelect = document.createElement("select");
      [10, 20, 50, 100, 200].forEach(function (n) {
        var opt = document.createElement("option");
        opt.value = n;
        opt.textContent = n;
        if (pageSize === n) opt.selected = true;
        sizeSelect.appendChild(opt);
      });
      var allOpt = document.createElement("option");
      allOpt.value = "all";
      allOpt.textContent = "All";
      if (pageSize === "all") allOpt.selected = true;
      sizeSelect.appendChild(allOpt);
      sizeSelect.addEventListener("change", function () {
        pageSize = sizeSelect.value === "all" ? "all" : parseInt(sizeSelect.value, 10);
        page = 1;
        render();
      });
      pager.appendChild(sizeSelect);

      var nav = document.createElement("div");
      nav.className = "dt-nav";
      var prev = document.createElement("button");
      prev.type = "button";
      prev.textContent = "Prev";
      prev.disabled = page <= 1;
      prev.addEventListener("click", function () { page--; render(); });
      var next = document.createElement("button");
      next.type = "button";
      next.textContent = "Next";
      next.disabled = page >= totalPages;
      next.addEventListener("click", function () { page++; render(); });
      var pageLabel = document.createElement("span");
      pageLabel.className = "dt-page-label";
      pageLabel.textContent = "Page " + page + " of " + totalPages;
      nav.appendChild(prev);
      nav.appendChild(pageLabel);
      nav.appendChild(next);
      pager.appendChild(nav);
    }

    function setColumnVisible(idx, visible) {
      table.querySelectorAll("tr").forEach(function (r) {
        var cell = r.cells[idx];
        if (cell) cell.style.display = visible ? "" : "none";
      });
    }

    function loadView() {
      try {
        return JSON.parse(sessionStorage.getItem(viewKey)) || {};
      } catch (e) {
        return {};
      }
    }
    function saveView(extra) {
      var v = { page: page, pageSize: pageSize, sortIndex: sortState.index, sortDir: sortState.dir };
      if (extra) for (var k in extra) v[k] = extra[k];
      try {
        sessionStorage.setItem(viewKey, JSON.stringify(v));
      } catch (e) {}
    }

    function loadHiddenCols() {
      try {
        return JSON.parse(localStorage.getItem(storageKey + ":hidden")) || {};
      } catch (e) {
        return {};
      }
    }
    function saveHiddenCols(v) {
      try {
        localStorage.setItem(storageKey + ":hidden", JSON.stringify(v));
      } catch (e) {}
    }

    if (typeof saved.sortIndex === "number" && saved.sortIndex >= 0) applySort(saved.sortIndex, saved.sortDir === -1 ? -1 : 1);
    if (saved.page > 0) page = saved.page;
    render();

    // Remember where the page was scrolled when leaving it (a row's form
    // submit, a link), and put it back after the redirect brings us here.
    window.addEventListener("pagehide", function () { saveView({ scrollY: window.scrollY }); });
    if (saved.scrollY > 0) window.scrollTo(0, saved.scrollY);
  }

  // stripFlash drops the one-shot ?flash= message so a page keeps the same
  // remembered view with or without it.
  function stripFlash(search) {
    var params = new URLSearchParams(search);
    params.delete("flash");
    var s = params.toString();
    return s ? "?" + s : "";
  }

  function initBulkBar() {
    var table = document.getElementById("devices-table");
    var bar = document.getElementById("bulk-bar");
    if (!table || !bar) return;
    var selectAll = document.getElementById("select-all");
    var countEl = document.getElementById("bulk-count");

    function rowChecks() {
      return Array.prototype.slice.call(table.querySelectorAll(".row-select"));
    }

    function refresh() {
      var checked = rowChecks().filter(function (c) { return c.checked; });
      bar.hidden = checked.length === 0;
      if (countEl) countEl.textContent = checked.length + " selected";
    }

    if (selectAll) {
      selectAll.addEventListener("change", function () {
        rowChecks().forEach(function (c) { c.checked = selectAll.checked; });
        refresh();
      });
    }
    table.addEventListener("change", function (e) {
      if (e.target.classList.contains("row-select")) refresh();
    });
    refresh();
  }

  document.querySelectorAll("[data-datatable]").forEach(init);
  initBulkBar();
})();
