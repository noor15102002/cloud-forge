"use strict";

const net = require("node:net");
const os = require("node:os");

const HEARTBEAT_KEY = "cloudforge:worker:heartbeat";
const OWNER_KEY = "cloudforge:fixture:first-worker";
const CONTROL_TTL_SECONDS = 3600;
const MODES = new Set(["healthy", "never", "stale", "frozen", "future", "malformed", "exit", "first-pod-only", "fail-second-start"]);
const ORDINAL_CLAIM = `
local field = "pod:" .. ARGV[1]
local ordinal = redis.call("HGET", KEYS[1], field)
if ordinal then return tonumber(ordinal) end
ordinal = redis.call("HINCRBY", KEYS[1], "sequence", 1)
redis.call("HSET", KEYS[1], field, ordinal)
redis.call("EXPIRE", KEYS[1], ARGV[2])
return ordinal
`;
const FIRST_OWNER_CLAIM = `
local owner = redis.call("GET", KEYS[1])
if not owner then
  redis.call("SET", KEYS[1], ARGV[1], "EX", ARGV[2])
  return 1
end
if owner == ARGV[1] then return 1 end
return 0
`;

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

async function claimControl(endpoint, mode, owner, send = command) {
  const script = mode === "fail-second-start" ? ORDINAL_CLAIM : FIRST_OWNER_CLAIM;
  const values = ["EVAL", script, "1", OWNER_KEY, owner, CONTROL_TTL_SECONDS];
  const first = await send(endpoint, values);
  // Repeat the real atomic claim before using its reply. The matrix therefore
  // tests the same per-pod request after completion, as a lost reply would need.
  const repeated = await send(endpoint, values);
  if (!Number.isSafeInteger(first) || first !== repeated || first < 0 ||
      (mode === "fail-second-start" ? first < 1 : first > 1)) {
    throw new Error("fixture control claim was not stable");
  }
  return first;
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
  let ordinal;
  let ownsFirstPod;
  while (!stopping) {
    try {
      if (mode === "fail-second-start" && ordinal === undefined) {
        ordinal = await claimControl(endpoint, mode, os.hostname());
      }
      if (mode === "first-pod-only" && ownsFirstPod === undefined) {
        // A replacement remains alive but never publishes. The old heartbeat
        // may persist for its remaining TTL, so it must not prove new startup.
        ownsFirstPod = await claimControl(endpoint, mode, os.hostname()) === 1;
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

module.exports = { encodeCommand, timestampFor, claimControl };
