#!/usr/bin/env python3
"""Assert openapi.json still declares every path and schema Lidarr relies on.

Lives in a file rather than inline in the workflow on purpose: as an inline
`python3 -c "..."` inside a YAML block scalar its body has to stay indented
deeper than the `run:` key, and it did not - api-check.yml was invalid YAML
from the day it was added, so every run ended in a startup failure after ~0s
with no log, which reads exactly like a billing or runner problem.
"""

import json
import sys

REQUIRED_PATHS = ["/api", "/api/newznab", "/api/sabnzbd", "/health"]

REQUIRED_SCHEMAS = [
    "VersionResponse",
    "QueueResponse",
    "Queue",
    "Slot",
    "HistoryResponse",
    "History",
    "HistorySlot",
    "ConfigResponse",
    "Config",
    "Category",
    "Misc",
    "StatusResponse",
    "AddURLResponse",
    "FullStatusResponse",
    "ServerStatsResponse",
    "WarningsResponse",
    "RetryResponse",
    "NewznabRSS",
]


def main() -> int:
    with open("openapi.json") as f:
        spec = json.load(f)

    paths = spec.get("paths", {})
    schemas = spec.get("components", {}).get("schemas", {})

    missing = [p for p in REQUIRED_PATHS if p not in paths]
    missing += [f"schemas/{s}" for s in REQUIRED_SCHEMAS if s not in schemas]
    if missing:
        for m in missing:
            print(f"missing from openapi.json: {m}", file=sys.stderr)
        return 1

    print(
        f"OpenAPI spec valid: {len(paths)} paths, {len(schemas)} schemas, "
        f"{len(REQUIRED_PATHS)} required paths and "
        f"{len(REQUIRED_SCHEMAS)} required schemas present"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
