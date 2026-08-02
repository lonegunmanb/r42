#!/usr/bin/env python3
"""Perplexity external tools packaged by the r42 multi-step module."""

from __future__ import annotations

import datetime as dt
import json
import os
import pathlib
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Any


API_BASE_URL = "https://api.perplexity.ai"
MAX_RESPONSE_BYTES = 10 << 20
REQUEST_TIMEOUT_SECONDS = 60


class ToolError(Exception):
    """An infrastructure failure that should fail the external tool process."""


def read_input() -> dict[str, Any]:
    try:
        value = json.load(sys.stdin)
    except json.JSONDecodeError as error:
        raise ToolError(f"decode tool input: {error}") from error
    if not isinstance(value, dict):
        raise ToolError("tool input must be a JSON object")
    return value


def write_response(response: dict[str, Any]) -> None:
    json.dump(response, sys.stdout, ensure_ascii=False, separators=(",", ":"))
    sys.stdout.write("\n")


def reject(path: str, message: str) -> dict[str, Any]:
    return {
        "accepted": False,
        "issues": [
            {
                "code": "invalid_arguments",
                "message": message,
                "path": path,
            }
        ],
    }


def required_string(arguments: dict[str, Any], name: str) -> str:
    value = arguments.get(name)
    if not isinstance(value, str) or not value.strip():
        return ""
    return value.strip()


def post_json(path: str, payload: dict[str, Any]) -> dict[str, Any]:
    api_key = os.environ.get("PPLX_API_KEY", "").strip()
    if not api_key:
        raise ToolError("PPLX_API_KEY is required")

    request = urllib.request.Request(
        urllib.parse.urljoin(API_BASE_URL + "/", path.lstrip("/")),
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT_SECONDS) as response:
            body = response.read(MAX_RESPONSE_BYTES + 1)
    except urllib.error.HTTPError as error:
        body = error.read(MAX_RESPONSE_BYTES)
        detail = body.decode("utf-8", errors="replace").strip()
        raise ToolError(f"Perplexity API status {error.code}: {detail}") from error
    except urllib.error.URLError as error:
        raise ToolError(f"call Perplexity API: {error.reason}") from error

    if len(body) > MAX_RESPONSE_BYTES:
        raise ToolError("Perplexity API response exceeded 10 MiB")
    try:
        decoded = json.loads(body)
    except json.JSONDecodeError as error:
        raise ToolError(f"decode Perplexity API response: {error}") from error
    if not isinstance(decoded, dict):
        raise ToolError("Perplexity API response must be a JSON object")
    return decoded


def clean_search_result(value: Any) -> dict[str, str] | None:
    if not isinstance(value, dict):
        return None
    result = {
        "title": str(value.get("title") or "").strip(),
        "url": str(value.get("url") or "").strip(),
        "snippet": str(value.get("snippet") or "").strip(),
        "date": str(value.get("date") or "").strip(),
        "last_updated": str(value.get("last_updated") or "").strip(),
        "source": str(value.get("source") or "").strip(),
    }
    if not result["url"] and not result["title"] and not result["snippet"]:
        return None
    return result


def search(arguments: dict[str, Any]) -> dict[str, Any]:
    query = required_string(arguments, "query")
    if not query:
        return reject("query", "query is required")

    decoded = post_json(
        "/search",
        {
            "query": query,
            "max_results": 10,
            "search_context_size": "high",
        },
    )
    results = []
    seen_urls: set[str] = set()
    for value in decoded.get("results", []):
        result = clean_search_result(value)
        if result is None or result["url"] in seen_urls:
            continue
        seen_urls.add(result["url"])
        results.append(result)
    return {"accepted": True, "output": {"results": results}}


def content_text(value: Any) -> str:
    if isinstance(value, str):
        return value.strip()
    if not isinstance(value, list):
        return ""
    parts = []
    for block in value:
        if isinstance(block, dict) and isinstance(block.get("text"), str):
            text = block["text"].strip()
            if text:
                parts.append(text)
    return "\n\n".join(parts)


def extract_fetched_content(decoded: dict[str, Any], requested_url: str) -> tuple[str, str, str]:
    fallback = ""
    for item in decoded.get("output", []):
        if not isinstance(item, dict):
            continue
        contents = item.get("contents")
        if isinstance(contents, list):
            for value in contents:
                result = clean_search_result(value)
                if result is not None and result["snippet"]:
                    return (
                        result["title"] or requested_url,
                        result["url"] or requested_url,
                        result["snippet"],
                    )
        text = content_text(item.get("content"))
        if text and not fallback:
            fallback = text
    if fallback:
        return requested_url, requested_url, fallback
    raise ToolError("Perplexity fetch returned no extracted content")


def fetch(arguments: dict[str, Any]) -> dict[str, Any]:
    source_url = required_string(arguments, "url")
    parsed = urllib.parse.urlparse(source_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        return reject("url", "url must be an absolute HTTP or HTTPS URL")

    decoded = post_json(
        "/v1/agent",
        {
            "model": "perplexity/sonar",
            "input": f"Fetch and extract the main content from this URL: {source_url}",
            "instructions": (
                "Use fetch_url for the provided URL. Return only information "
                "grounded in the fetched content."
            ),
            "tools": [{"type": "fetch_url", "max_urls": 1}],
            "max_steps": 2,
        },
    )
    title, fetched_url, content = extract_fetched_content(decoded, source_url)
    fetched_at = dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")
    snapshot_path = (pathlib.Path.cwd() / "snapshot.md").resolve()
    snapshot = "\n".join(
        [
            f"# {title}",
            "",
            f"- URL: {fetched_url}",
            f"- Fetched at: {fetched_at}",
            "- Snapshot source: Perplexity fetch_url",
            "",
            "## Extracted Content",
            "",
            content.strip(),
            "",
        ]
    )
    snapshot_path.write_text(snapshot, encoding="utf-8")
    return {
        "accepted": True,
        "output": {
            "title": title,
            "url": fetched_url,
            "snapshot_path": snapshot_path.as_posix(),
            "fetched_at": fetched_at,
        },
    }


def main() -> int:
    if len(sys.argv) != 2 or sys.argv[1] not in {"search", "fetch"}:
        print("usage: pplx_external.py {search|fetch}", file=sys.stderr)
        return 2
    try:
        arguments = read_input()
        response = search(arguments) if sys.argv[1] == "search" else fetch(arguments)
        write_response(response)
        return 0
    except (OSError, ToolError) as error:
        print(str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
