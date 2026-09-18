"""Reference FastAPI service with explicit, disposable-test instrumentation."""
import asyncio
import os
import time

from fastapi import FastAPI
from fastapi.responses import JSONResponse

POD = os.environ.get("HOSTNAME", "local")
FAILURE = "shutdown"
ready = True
active = set()


app = FastAPI()

@app.get("/health")
async def health():
    return {"ok": True}


@app.get("/ready")
async def readiness():
    return JSONResponse({"ready": ready}, status_code=200 if ready or FAILURE == "readiness" else 503)


@app.get("/work")
async def work():
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
        return {"pod": POD, "ready": ready, "completed": id}
    finally:
        active.discard(id)
