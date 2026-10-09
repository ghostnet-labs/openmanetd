// =============================================================================
// fake-luci.mjs — Stand-in for uhttpd + LuCI in the browser suite
// =============================================================================
//
// The frontend server reverse-proxies /cgi-bin/, /luci-static/ and /ubus/ to
// frontend.luciProxy.upstream (uhttpd on 127.0.0.1:80 on a node). This server
// answers those three prefixes with just enough LuCI-shaped content for the
// e2e suite to prove the handoff works through the real Go proxy: an HTML
// page under /cgi-bin/luci/ that links back to the OpenMANET dashboard, a
// stylesheet under /luci-static/, and a JSON-RPC reply on /ubus/. It records
// nothing and holds no state. Node standard library only.

import http from 'node:http';

const port = Number(process.env.FAKE_LUCI_PORT || 18088);

const page = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OpenMANET - LuCI (e2e fake)</title>
<link rel="stylesheet" href="/luci-static/e2e/cascade.css">
</head>
<body>
<main>
<h1>LuCI (e2e fake upstream)</h1>
<p id="luci-path"></p>
<a id="back-to-openmanet" href="/">Back to OpenMANET</a>
</main>
</body>
</html>`;

const css = 'body{background:#111;color:#eee;font-family:monospace;padding:16px}a{color:#4fd1ff}\n';

function send(res, status, type, body, extra = {}) {
  res.writeHead(status, {
    'Content-Type': type,
    'Content-Length': Buffer.byteLength(body),
    'X-E2E-Fake-Luci': '1',
    ...extra,
  });
  res.end(body);
}

const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://fake-luci');
  if (url.pathname.startsWith('/cgi-bin/luci')) {
    // Same cookie scope uhttpd/LuCI use, so the proxy's cookie handling is
    // exercised on every page load.
    send(res, 200, 'text/html; charset=utf-8', page.replace('<p id="luci-path"></p>', `<p id="luci-path">${url.pathname}</p>`), {
      'Set-Cookie': 'sysauth_http=e2e; path=/cgi-bin/luci/; HttpOnly; SameSite=Strict',
    });
    return;
  }
  if (url.pathname.startsWith('/luci-static/')) {
    send(res, 200, 'text/css; charset=utf-8', css);
    return;
  }
  if (url.pathname.startsWith('/ubus')) {
    send(res, 200, 'application/json', JSON.stringify({ jsonrpc: '2.0', id: 1, result: [0, { e2e: true }] }));
    return;
  }
  send(res, 404, 'text/plain; charset=utf-8', 'not found\n');
});

server.listen(port, '127.0.0.1', () => {
  console.log(`fake LuCI upstream on http://127.0.0.1:${port}`);
});

for (const sig of ['SIGINT', 'SIGTERM']) {
  process.on(sig, () => server.close(() => process.exit(0)));
}
