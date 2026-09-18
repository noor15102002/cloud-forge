const http = require("node:http");

const version = process.env.CLOUDFORGE_VERSION || "a";
const server = http.createServer((request, response) => {
  if (request.url === "/ready") {
    response.writeHead(version === "b" ? 503 : 200, { "content-type": "text/plain" });
    response.end(`${version}\n`);
    return;
  }
  if (request.url === "/health") {
    response.writeHead(200, { "content-type": "text/plain" });
    response.end(`${version}\n`);
    return;
  }
  response.writeHead(404).end();
});

server.listen(8080, "0.0.0.0");

process.on("SIGTERM", () => server.close(() => process.exit(0)));
