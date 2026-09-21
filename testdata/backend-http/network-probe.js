"use strict";

// Disposable qualification helper, never used by the application's HTTP process.
const net = require("node:net");
const mode = process.argv[2];
if (mode === "listen") {
  const server = net.createServer(socket => {
    // TCP readiness and connectivity probes may reset immediately after connect.
    // A client reset must not kill the positive control for later deny checks.
    socket.on("error", () => socket.destroy());
    socket.end("canary\n");
  });
  const host = process.argv[3] ?? "0.0.0.0";
  const port = Number(process.argv[4] ?? 8081);
  server.listen(port, host, () => {
    const address = server.address();
    process.stdout.write(JSON.stringify({ listening: true, address: address.address, port: address.port }) + "\n");
  });
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
