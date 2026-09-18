const http = require("node:http");

const server = http.createServer((request, response) => {
  if (request.url === "/health" || request.url === "/ready") {
    response.writeHead(200, { "content-type": "text/plain" });
    response.end("ok\n");
    return;
  }
  response.writeHead(404).end();
});

server.listen(8080, "0.0.0.0");

process.on("SIGTERM", () => process.exit(1));
