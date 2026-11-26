(() => {
  try { document.documentElement.dataset.theme = localStorage.getItem('flowops_theme') === 'dark' ? 'dark' : 'light'; }
  catch { document.documentElement.dataset.theme = 'light'; }
})();
