const http = require("node:http");

const server = http.createServer((request, response) => {
  if (request.url === "/health" || request.url === "/ready") {
    response.writeHead(200, { "content-type": "text/plain" });
    response.end("ok\n");
    return;
  }
  response.writeHead(404, { "content-type": "text/plain" });
  response.end("not found\n");
});

server.listen(8080, "0.0.0.0");

process.on("SIGTERM", () => {
  server.close(() => process.exit(0));
});
