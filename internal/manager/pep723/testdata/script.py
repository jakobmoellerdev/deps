#!/usr/bin/env -S uv run --script
# The example of PEP 723 (https://peps.python.org/pep-0723/#example), extended.
# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "requests<3",
#   "rich",
#   "httpx[http2]>=0.27.0,<1",
#   "pkg @ git+https://github.com/example/pkg",
#   "click==8.1.7",
# ]
#
# [tool.uv]
# exclude-newer = "2024-11-01T00:00:00Z"
# ///

import requests
from rich.pretty import pprint

resp = requests.get("https://peps.python.org/api/peps.json")
data = resp.json()
pprint([(k, v["title"]) for k, v in data.items()][:10])
