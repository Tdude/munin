(function () {
  var script = document.currentScript;
  if (!script) return;
  var siteId = script.getAttribute('data-site-id');
  if (!siteId) return;

  var endpoint = script.getAttribute('data-endpoint');
  if (!endpoint) {
    try {
      // Derive /collect relative to script.src so subpath hosting
      // (e.g. https://obojen.com/muntra/script.js → /muntra/collect) works
      // without an explicit data-endpoint attribute.
      var u = new URL(script.src);
      u.search = '';
      u.pathname = u.pathname.replace(/\/[^/]*$/, '/collect');
      endpoint = u.toString();
    } catch (e) {
      return;
    }
  }

  function send(name, data) {
    var tz = '';
    try { tz = Intl.DateTimeFormat().resolvedOptions().timeZone || ''; } catch (e) {}
    try {
      var body = JSON.stringify({
        site_id: siteId,
        url: location.href,
        referrer: document.referrer,
        screen: screen.width + 'x' + screen.height,
        viewport: (window.innerWidth || 0) + 'x' + (window.innerHeight || 0),
        timezone: tz,
        pixel_ratio: window.devicePixelRatio || 1,
        language: navigator.language,
        title: document.title,
        name: name || 'pageview',
        data: data || null
      });
      if (navigator.sendBeacon) {
        navigator.sendBeacon(endpoint, new Blob([body], { type: 'application/json' }));
      } else {
        fetch(endpoint, {
          method: 'POST',
          body: body,
          headers: { 'Content-Type': 'application/json' },
          keepalive: true
        });
      }
    } catch (e) {}
  }

  var lastPath = location.pathname + location.search;
  function onUrlChange() {
    var cur = location.pathname + location.search;
    if (cur !== lastPath) {
      lastPath = cur;
      send('pageview');
    }
  }

  var push = history.pushState;
  history.pushState = function () {
    push.apply(this, arguments);
    onUrlChange();
  };
  var rep = history.replaceState;
  history.replaceState = function () {
    rep.apply(this, arguments);
    onUrlChange();
  };
  window.addEventListener('popstate', onUrlChange);

  send('pageview');

  window.muntra = { track: send };
})();
