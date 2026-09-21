"use strict";

// Disposable qualification helper, never used by the application's HTTP process.
const net = require("node:net");
const mode = process.argv[2];
if (mode === "listen") {
  net.createServer(socket => socket.end("canary\n")).listen(8081, "0.0.0.0");
} else if (mode === "idle") {
  setInterval(() => {}, 60_000);
} else if (mode === "tcp") {
  let finished = false;
  const socket = net.createConnection({ host: process.argv[3], port: Number(process.argv[4]) });
  const finish = connected => {
    if (finished) return;
    finished = true;
    socket.destroy();
    process.stdout.write(JSON.stringify({ connected }) + "\n");
  };
  socket.setTimeout(1200, () => finish(false));
  socket.on("error", () => finish(false));
  socket.on("connect", () => finish(true));
} else {
  process.exitCode = 2;
}
