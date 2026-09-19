"""Reference FastAPI service with explicit, disposable-test instrumentation."""
import asyncio
import os
import time

import redis.asyncio as redis

from fastapi import FastAPI
from fastapi.responses import JSONResponse

POD = os.environ.get("HOSTNAME", "local")
FAILURE = "none"
ready = True
active = set()
sigterm_active = set()


app = FastAPI()
client = redis.from_url(os.environ["REDIS_URL"], socket_connect_timeout=0.5, socket_timeout=0.5)

async def redis_ready():
    if os.environ.get("FORCE_DISCONNECTED") == "true":
        return False
    try:
        return bool(await client.ping())
    except Exception:
        return False

@app.get("/health")
async def health():
    return {"ok": True}


@app.get("/ready")
async def readiness():
    available = ready and await redis_ready() and os.environ.get("FORCE_DEGRADED") != "true"
    return JSONResponse({"status": "ready" if available else "degraded"}, status_code=200 if available or os.environ.get("FORCE_DEGRADED") == "true" else 503)


@app.get("/work")
async def work():
    if not await redis_ready():
        return JSONResponse({"status": "degraded"}, status_code=503)
    deadline = time.monotonic() + 0.005
    while time.monotonic() < deadline:
        pass
    return {"result": "completed"}


@app.get("/_test/identity")
@app.get("/_test/state")
async def state():
    return {"pod": POD, "ready": ready, "active": sorted(active)}


@app.get("/_test/unready")
async def unready():
    global ready
    ready = False
    return await state()


@app.get("/_test/ready")
async def restore():
    global ready
    ready = True
    return await state()


@app.get("/_test/slow")
async def slow(id: str):
    active.add(id)
    try:
        await asyncio.sleep(5)
        return {"pod": POD, "ready": ready, "completed": id, "sigterm_received": id in sigterm_active}
    finally:
        active.discard(id)
