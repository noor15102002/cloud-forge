const http = require("node:http");

let stopping = false;

const server = http.createServer((request, response) => {
  const path = new URL(request.url, "http://127.0.0.1").pathname;
  let status = 200;
  let body;
  if (request.method !== "GET") {
    status = 405;
    body = { error: "method not allowed" };
  } else if (path === "/health") {
    body = { ok: true };
  } else if (path === "/ready") {
    body = { ready: true };
  } else if (path === "/work") {
    body = { message: "Hello from the CloudForge HTTP example." };
  } else {
    status = 404;
    body = { error: "not found" };
  }
  response.writeHead(status, { "content-type": "application/json" });
  response.end(JSON.stringify(body));
});

server.listen(8080, "0.0.0.0");

function shutdown() {
  if (stopping) return;
  stopping = true;
  // Kubernetes withdraws a terminating pod from Service routing asynchronously.
  // Keep serving truthfully during propagation, then close and drain the listener.
  setTimeout(() => server.close(() => process.exit(0)), 2000);
  setTimeout(() => process.exit(1), 10000).unref();
}

process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);
