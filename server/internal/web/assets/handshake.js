// mAPI-ng handshake poller — while onboarding is incomplete, refresh just the
// #handshake stepper in place (no full-page reload) every 10s (the flush window),
// and stop as soon as the server signals the first summary landed. Mirrors copy.js: no framework,
// served self-hosted under CSP script-src 'self'. Progressive enhancement — the
// <noscript> meta-refresh advances onboarding when this never runs.
(function () {
  var el = document.getElementById('handshake');
  if (!el || el.getAttribute('data-complete') === 'true') return;
  var timer = setInterval(function () {
    fetch('/setup/handshake', { headers: { Accept: 'text/html' } })
      .then(function (resp) {
        if (!resp.ok) return;
        var done = resp.headers.get('X-Handshake-Complete') === 'true';
        return resp.text().then(function (html) {
          el.innerHTML = html;
          if (!done) return;
          el.setAttribute('data-complete', 'true');
          clearInterval(timer);
          // On the get-started page the first summary means the live dashboard is
          // ready — surface it. On /setup we simply stop polling.
          var p = window.location.pathname;
          if (p === '/' || p === '/dashboard') location.reload();
        });
      })
      .catch(function () {});
  }, 10000);
})();
