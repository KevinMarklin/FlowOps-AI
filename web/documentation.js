// Keep the chapter text immediately reachable on smaller screens.
(() => {
  const contents = document.querySelector('.docs-nav details');
  if (!contents) return;
  const narrow = window.matchMedia('(max-width: 850px)');
  contents.open = !narrow.matches;
  narrow.addEventListener('change', event => { contents.open = !event.matches; });
})();
