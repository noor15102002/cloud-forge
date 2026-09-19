const net = require("node:net");
const http = require("node:http");
const os = require("node:os");
const pod = os.hostname();
const failure = "none";
const version = process.env.CLOUDFORGE_VERSION || "a";
let ready = true;
const active = new Set();
const sigtermActive = new Set();
async function redisReady() {
  if (process.env.FORCE_DISCONNECTED === "true") return false;
  const endpoint = new URL(process.env.REDIS_URL);
  return new Promise(resolve => {
    const socket = net.createConnection({ host: endpoint.hostname, port: Number(endpoint.port) });
    let done = false;
    const finish = value => { if (!done) { done = true; socket.destroy(); resolve(value); } };
    socket.setTimeout(500, () => finish(false));
    socket.on("error", () => finish(false));
    socket.on("connect", () => socket.write("*1\r\n$4\r\nPING\r\n"));
    let received = "";
    socket.on("data", data => { received += data; if (received.includes("\r\n")) finish(received === "+PONG\r\n"); });
  });
}
const server = http.createServer(async (request, response) => {
  const url = new URL(request.url, "http://127.0.0.1");
  const send = (value, status = 200) => {
    response.writeHead(status, { "content-type": "application/json" });
    response.end(JSON.stringify(value));
  };
  const state = () => ({ pod, ready, active: [...active].sort() });
  if (url.pathname === "/_test/unready") { ready = false; return send(state()); }
  if (url.pathname === "/_test/ready") { ready = true; return send(state()); }
  if (url.pathname === "/_test/identity" || url.pathname === "/_test/state") return send(state());
  if (url.pathname === "/_test/slow") {
    const id = url.searchParams.get("id");
    active.add(id);
    return setTimeout(() => { active.delete(id); send({ pod, ready, completed: id, sigterm_received: sigtermActive.has(id) }); }, 5000);
  }
  if (url.pathname === "/ready") {
    const available = ready && await redisReady() && process.env.FORCE_DEGRADED !== "true";
    return send({ ...state(), status: available ? "ready" : "degraded" }, available || process.env.FORCE_DEGRADED === "true" ? 200 : 503);
  }
  if (url.pathname === "/health") return send({ ok: true });
  if (url.pathname === "/work") {
    if (!await redisReady()) return send({ status: "degraded" }, 503);
    const deadline = process.hrtime.bigint() + 5_000_000n;
    while (process.hrtime.bigint() < deadline) { /* bounded representative work */ }
    return send({ result: "completed" });
  }
  send({ error: "not found" }, 404);
});
server.listen(8080, "0.0.0.0");
process.on("SIGTERM", () => {
  for (const id of active) sigtermActive.add(id);
  if (failure === "shutdown") process.exit(1);
  setTimeout(() => server.close(() => process.exit(0)), 2000);
});
