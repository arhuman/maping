// mAPI-ng copy helper — the dashboard's entire client-side JS budget (CONTEXT:
// SSR/no-JS, one exception). Delegated clicks on [data-copy] buttons copy the
// text of the element whose id is the attribute value. Copy-only, no client
// state. Served from /assets/copy.js under CSP script-src 'self', so this is the
// only script the browser will execute.
document.addEventListener('click', function (e) {
  var btn = e.target.closest('[data-copy]');
  if (!btn || !navigator.clipboard) return;
  var target = document.getElementById(btn.getAttribute('data-copy'));
  if (!target) return;
  navigator.clipboard.writeText(target.textContent.trim()).then(function () {
    var prev = btn.textContent;
    btn.textContent = 'copied';
    setTimeout(function () { btn.textContent = prev; }, 1500);
  });
});
