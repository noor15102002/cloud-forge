"use strict";

const net = require("node:net");
const os = require("node:os");

const HEARTBEAT_KEY = "cloudforge:worker:heartbeat";
const OWNER_KEY = "cloudforge:fixture:first-worker";
const CONTROL_TTL_SECONDS = 3600;
const MODES = new Set(["healthy", "never", "stale", "frozen", "future", "malformed", "exit", "first-pod-only", "fail-second-start"]);

function encodeCommand(values) {
  return "*" + values.length + "\r\n" + values.map(value => {
    const text = String(value);
    return "$" + Buffer.byteLength(text) + "\r\n" + text + "\r\n";
  }).join("");
}

function command(endpoint, values) {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection({ host: endpoint.hostname, port: Number(endpoint.port || 6379) });
    let finished = false;
    let received = Buffer.alloc(0);
    const finish = (error, result) => {
      if (finished) return;
      finished = true;
      socket.destroy();
      if (error) reject(new Error("bounded Redis operation failed"));
      else resolve(result);
    };
    socket.setTimeout(1000, () => finish(true));
    socket.on("error", () => finish(true));
    socket.on("end", () => finish(true));
    socket.on("connect", () => socket.write(encodeCommand(values)));
    socket.on("data", data => {
      received = Buffer.concat([received, data]);
      if (received.length > 4096) return finish(true);
      const text = received.toString("utf8");
      const line = text.indexOf("\r\n");
      if (line < 0) return;
      if (text[0] === "+") return finish(false, text.slice(1, line));
      if (text[0] === ":") return finish(false, Number(text.slice(1, line)));
      if (text.startsWith("$-1\r\n")) return finish(false, null);
      finish(true);
    });
  });
}

function timestampFor(mode, now, firstTimestamp) {
  if (mode === "stale") return new Date(now - 120000).toISOString();
  if (mode === "future") return new Date(now + 120000).toISOString();
  if (mode === "frozen") return firstTimestamp;
  return new Date(now).toISOString();
}

async function main() {
  const mode = process.argv[2] || "healthy";
  if (!MODES.has(mode)) throw new Error("unsupported public fixture mode");
  if (mode === "exit") {
    process.exitCode = 23;
    return;
  }
  const endpoint = new URL(process.env.REDIS_URL);
  if (endpoint.protocol !== "redis:" || endpoint.username || endpoint.password) {
    throw new Error("fixture requires the isolated test Redis provider");
  }
  let stopping = false;
  process.once("SIGTERM", () => { stopping = true; });
  process.once("SIGINT", () => { stopping = true; });
  const firstTimestamp = new Date().toISOString();
  let ordinal = 1;
  if (mode === "fail-second-start") {
    ordinal = await command(endpoint, ["INCR", OWNER_KEY]);
    await command(endpoint, ["EXPIRE", OWNER_KEY, CONTROL_TTL_SECONDS]);
  }
  let ownsFirstPod;
  while (!stopping) {
    try {
      if (mode === "first-pod-only" && ownsFirstPod === undefined) {
        // A replacement remains alive but never publishes. The old heartbeat
        // may persist for its remaining TTL, so it must not prove new startup.
        ownsFirstPod = await command(endpoint, ["SET", OWNER_KEY, os.hostname(), "NX", "EX", CONTROL_TTL_SECONDS]) === "OK";
      }
      if (mode !== "never" && (mode !== "first-pod-only" || ownsFirstPod) && (mode !== "fail-second-start" || ordinal !== 2)) {
        const payload = mode === "malformed" ? "{not-valid-json" : JSON.stringify({ at: timestampFor(mode, Date.now(), firstTimestamp),
          pod: os.hostname(), version: process.env.CLOUDFORGE_VERSION || "a" });
        if (await command(endpoint, ["SET", HEARTBEAT_KEY, payload, "EX", "3"]) !== "OK") {
          throw new Error("heartbeat was not acknowledged");
        }
      }
    } catch {
      // No URLs, credentials, server replies or arbitrary source text are logged.
      // CloudForge observes the resulting heartbeat absence through its contract.
    }
    await new Promise(resolve => setTimeout(resolve, 1000));
  }
}

if (require.main === module) {
  main().catch(() => { process.exitCode = 24; });
}

module.exports = { encodeCommand, timestampFor };
