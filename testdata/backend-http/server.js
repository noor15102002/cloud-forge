"use strict";

const http = require("node:http");
const net = require("node:net");
const os = require("node:os");
const { Pool } = require("pg");

const key = process.env.APP_TEST_KEY || "";
if (!/^[A-Za-z0-9+/]+={0,2}$/.test(key) || Buffer.from(key, "base64").length !== 32) {
  console.error("generated-test-key-required");
  process.exit(1);
}

const pool = new Pool({ connectionString: process.env.DATABASE_URL, max: 4, connectionTimeoutMillis: 1500, query_timeout: 1500 });
pool.on("error", () => {});
const pod = os.hostname();
const version = process.env.CLOUDFORGE_VERSION || "a";
let ready = true;
const active = new Set();
const sigtermActive = new Set();

function exchange(host, port, request, expected) {
  return new Promise(resolve => {
    let finished = false;
    let received = "";
    const socket = net.createConnection({ host, port });
    const finish = value => {
      if (finished) return;
      finished = true;
      socket.destroy();
      resolve(value);
    };
    socket.setTimeout(1200, () => finish(false));
    socket.on("error", () => finish(false));
    socket.on("connect", () => socket.write(request));
    socket.on("data", data => {
      received += data.toString("utf8");
      if (received.length > 1024) return finish(false);
      if (received.includes("\n") || received.includes("\0")) finish(received.trim().replace(/\0$/, "") === expected);
    });
    socket.on("end", () => finish(received.trim().replace(/\0$/, "") === expected));
  });
}

async function dependenciesReady() {
  try {
    const redis = new URL(process.env.REDIS_URL);
    const [postgres, redisOK, clamavOK] = await Promise.all([
      pool.query("SELECT embedding <-> '[0,0,0]'::vector AS distance FROM cloudforge_probe WHERE id = 1"),
      exchange(redis.hostname, Number(redis.port) || 6379, "*1\r\n$4\r\nPING\r\n", "+PONG"),
      exchange(process.env.CLAMAV_HOST, Number(process.env.CLAMAV_PORT), "zPING\0", "PONG"),
    ]);
    return Number(postgres.rows[0]?.distance) === 5 && redisOK && clamavOK;
  } catch {
    return false;
  }
}

const server = http.createServer(async (request, response) => {
  const url = new URL(request.url, "http://127.0.0.1");
  const state = () => ({ pod, ready, active: [...active].sort() });
  const send = (body, code = 200) => {
    response.writeHead(code, { "content-type": "application/json", "cache-control": "no-store" });
    response.end(JSON.stringify(body));
  };
  if (url.pathname === "/_test/unready") { ready = false; return send(state()); }
  if (url.pathname === "/_test/ready") { ready = true; return send(state()); }
  if (["/_test/identity", "/_test/state"].includes(url.pathname)) return send(state());
  if (url.pathname === "/_test/slow") {
    const id = url.searchParams.get("id");
    if (!id || id.length > 128 || active.size >= 8) return send({ status: "invalid" }, 400);
    active.add(id);
    return setTimeout(() => {
      active.delete(id);
      send({ ...state(), completed: id, sigterm_received: sigtermActive.has(id) });
    }, 5000);
  }
  if (url.pathname === "/health") return send({ status: "ok", service: "backend-http", version });
  if (url.pathname === "/ready" || url.pathname === "/work") {
    const healthy = ready && await dependenciesReady();
    return send({ ...state(), status: healthy ? "ok" : "unavailable", service: "backend-http", version }, healthy ? 200 : 503);
  }
  return send({ status: "not_found" }, 404);
});

async function start() {
  if (!await dependenciesReady()) {
    console.error("prepared-backend-required");
    await pool.end();
    process.exitCode = 1;
    return;
  }
  server.listen(8080, "0.0.0.0");
}
process.on("SIGTERM", () => {
  for (const id of active) sigtermActive.add(id);
  setTimeout(() => server.close(async () => { await pool.end(); process.exit(0); }), 2000);
});
start().catch(() => { console.error("startup-failed"); process.exitCode = 1; });
