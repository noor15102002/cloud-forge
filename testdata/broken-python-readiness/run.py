"""Allow EndpointSlice removal to propagate before Uvicorn closes listeners."""
import asyncio
import os

import uvicorn
from app import FAILURE


class DrainingServer(uvicorn.Server):
    def handle_exit(self, sig, frame):
        if FAILURE == "shutdown":
            os._exit(1)
        asyncio.get_running_loop().call_later(2, super().handle_exit, sig, frame)


if __name__ == "__main__":
    DrainingServer(uvicorn.Config("app:app", host="0.0.0.0", port=8000,
                                 timeout_graceful_shutdown=15, access_log=False)).run()
