import math
import json

import uvicorn
from fastapi import FastAPI, HTTPException, Request

app = FastAPI()

MAX_BODY_BYTES = 1_048_576  # 1 MiB


@app.get("/health")
async def health():
    return {"status": "ok"}


@app.post("/v1/messages")
async def anthropic_messages(request: Request):
    """Anthropic Messages API simulator.

    Accepts Anthropic-format requests and returns Anthropic-format responses.
    Used when vSR routes to a model configured with api_format: anthropic.
    """
    # Enforce body size limit
    content_length = request.headers.get("content-length")
    if content_length and int(content_length) > MAX_BODY_BYTES:
        raise HTTPException(status_code=413, detail="Request body too large")

    try:
        body = await request.json()
    except json.JSONDecodeError:
        raise HTTPException(status_code=400, detail="Invalid JSON")

    if not isinstance(body, dict):
        raise HTTPException(status_code=400, detail="Request body must be a JSON object")

    model = body.get("model", "mock-anthropic")
    messages = body.get("messages", [])
    system_text = ""
    if "system" in body:
        sys = body["system"]
        if isinstance(sys, str):
            system_text = sys
        elif isinstance(sys, list):
            system_text = " ".join(
                b.get("text", "") for b in sys
                if isinstance(b, dict) and b.get("type") == "text"
            )

    user_messages = []
    if isinstance(messages, list):
        for msg in messages:
            if not isinstance(msg, dict):
                continue
            if msg.get("role") == "user":
                content = msg.get("content", "")
                if isinstance(content, str):
                    user_messages.append(content)
                elif isinstance(content, list):
                    for block in content:
                        if isinstance(block, dict) and block.get("type") == "text":
                            user_messages.append(block.get("text", ""))

    response_text = json.dumps(
        {
            "mock": "mock-anthropic",
            "model": model,
            "system": system_text,
            "user": user_messages,
            "total_messages": len(messages),
        },
        separators=(",", ":"),
        sort_keys=True,
    )

    def estimate_tokens(text: str) -> int:
        if not text:
            return 0
        return max(1, math.ceil(len(text) / 4))

    input_tokens = estimate_tokens(system_text + " ".join(user_messages))
    output_tokens = estimate_tokens(response_text)

    return {
        "id": "msg-mock-123",
        "type": "message",
        "role": "assistant",
        "content": [{"type": "text", "text": response_text}],
        "model": model,
        "stop_reason": "end_turn",
        "stop_sequence": None,
        "usage": {
            "input_tokens": input_tokens,
            "output_tokens": output_tokens,
        },
    }


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8000)
