(() => {
  const storageKey = 'flowops.documentation.expanded-groups';
  let expanded = {};
  try {
    const saved = JSON.parse(sessionStorage.getItem(storageKey) || '{}');
    if (saved && typeof saved === 'object' && !Array.isArray(saved)) expanded = saved;
  } catch (_) { /* Navigation remains usable when storage is unavailable. */ }
  document.querySelectorAll('.docs-nav-group').forEach(group => {
    const title = group.querySelector('summary').textContent.trim();
    if (expanded[title] === true) group.open = true;
    expanded[title] = group.open;
    group.addEventListener('toggle', () => {
      expanded[title] = group.open;
      try { sessionStorage.setItem(storageKey, JSON.stringify(expanded)); } catch (_) {}
    });
  });
  try { sessionStorage.setItem(storageKey, JSON.stringify(expanded)); } catch (_) {}
})();
