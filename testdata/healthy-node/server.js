const http = require("node:http");

const server = http.createServer((request, response) => {
  const parsed = new URL(request.url, "http://127.0.0.1");
  if (parsed.pathname === "/health" || parsed.pathname === "/ready") {
    if (parsed.searchParams.get("cloudforge_load") === "1") {
      const deadline = process.hrtime.bigint() + 5_000_000n;
      while (process.hrtime.bigint() < deadline) {
        // Deliberately bounded CPU work lets the fixture exercise its HPA.
      }
    }
    response.writeHead(200, { "content-type": "text/plain" });
    response.end("ok\n");
    return;
  }
  response.writeHead(404, { "content-type": "text/plain" });
  response.end("not found\n");
});

server.listen(8080, "0.0.0.0");

process.on("SIGTERM", () => {
  // Allow Kubernetes endpoint removal to propagate before closing listeners.
  setTimeout(() => server.close(() => process.exit(0)), 2000);
});
