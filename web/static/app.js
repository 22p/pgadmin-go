(function () {
  'use strict';

  var root = document.documentElement;

  // 主题切换
  function toggleTheme() {
    var next = root.dataset.theme === 'dark' ? 'light' : 'dark';
    root.dataset.theme = next;
    try { localStorage.setItem('pggo-theme', next); } catch (e) {}
  }
  document.querySelectorAll('[data-theme-toggle]').forEach(function (b) {
    b.addEventListener('click', toggleTheme);
  });

  // 移动端侧栏
  var sidebar = document.querySelector('[data-sidebar]');
  var backdrop = document.querySelector('.sidebar-backdrop');
  document.querySelectorAll('[data-sidebar-toggle]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      if (!sidebar) return;
      sidebar.classList.toggle('open');
      if (backdrop) backdrop.classList.toggle('show');
    });
  });

  // 对象树过滤
  var filter = document.getElementById('object-filter');
  if (filter) {
    filter.addEventListener('input', function () {
      var q = filter.value.trim().toLowerCase();
      document.querySelectorAll('.schema').forEach(function (box) {
        var shown = 0;
        box.querySelectorAll('.obj-list li').forEach(function (li) {
          var hit = !q || (li.dataset.object || '').toLowerCase().indexOf(q) !== -1;
          li.style.display = hit ? '' : 'none';
          if (hit) shown++;
        });
        box.style.display = shown ? '' : 'none';
        if (q && shown) box.open = true;
      });
    });
  }

  // 登录页认证方式切换
  var methods = document.querySelectorAll('input[name="method"]');
  function syncMethod() {
    var checked = document.querySelector('input[name="method"]:checked');
    var value = checked ? checked.value : 'password';
    document.querySelectorAll('[data-method-panel]').forEach(function (panel) {
      var list = (panel.dataset.methodPanel || '').split(' ');
      panel.classList.toggle('active', list.indexOf(value) !== -1);
    });
    document.querySelectorAll('[data-method-only]').forEach(function (el) {
      var list = (el.dataset.methodOnly || '').split(' ');
      el.style.display = list.indexOf(value) !== -1 ? '' : 'none';
    });
    // 证书方式强制 TLS:把 disable 切换为 require
    var ssl = document.querySelector('select[name="sslmode"]');
    if (ssl && value === 'cert' && ssl.value === 'disable') {
      ssl.value = 'require';
    }
  }
  if (methods.length) {
    methods.forEach(function (m) { m.addEventListener('change', syncMethod); });
    syncMethod();
  }

  // 危险操作确认
  document.querySelectorAll('form[data-confirm]').forEach(function (form) {
    form.addEventListener('submit', function (e) {
      if (!window.confirm(form.dataset.confirm)) e.preventDefault();
    });
  });


  // 筛选条件行
  var filterRows = document.querySelector('[data-filter-rows]');
  if (filterRows) {
    var filterTpl = document.querySelector('[data-filter-template]');
    var addFilterRow = function () {
      if (!filterTpl) return;
      var row = filterTpl.content.firstElementChild.cloneNode(true);
      filterRows.appendChild(row);
      var first = row.querySelector('input[name="f.val"]');
      if (first) first.focus();
    };
    if (!filterRows.children.length) addFilterRow();
    document.querySelectorAll('[data-add-filter]').forEach(function (btn) {
      btn.addEventListener('click', addFilterRow);
    });
  }

  // 建表 / 追加列
  var designRows = document.querySelector('[data-column-rows]');
  if (designRows) {
    var proto = designRows.querySelector('.design-row');
    document.querySelectorAll('[data-add-column]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        if (!proto) return;
        var row = proto.cloneNode(true);
        row.querySelectorAll('input').forEach(function (i) { i.value = ''; });
        row.querySelectorAll('select').forEach(function (s) { s.selectedIndex = 0; });
        designRows.appendChild(row);
        var first = row.querySelector('input');
        if (first) first.focus();
      });
    });
  }

  document.addEventListener('click', function (e) {
    var remove = e.target.closest('[data-remove-filter],[data-remove-column]');
    if (remove) {
      var row = remove.closest('.filter-row, .design-row');
      if (row) row.remove();
      return;
    }
    // 点击外部关闭筛选 / 列面板
    document.querySelectorAll('details.panel[open]').forEach(function (d) {
      if (!d.contains(e.target)) d.open = false;
    });
  });

  // 收藏当前编辑器中的 SQL
  document.querySelectorAll('form[data-copy-sql]').forEach(function (form) {
    form.addEventListener('submit', function () {
      var editor = document.getElementById('editor');
      var field = form.querySelector('input[name="sql"]');
      if (editor && field) field.value = editor.value;
    });
  });

  // SQL 快捷键
  var editor = document.getElementById('editor');
  if (editor) {
    editor.addEventListener('keydown', function (e) {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault();
        if (editor.form) editor.form.submit();
      }
    });
  }
})();
