"""Where the client keeps its own files, resolved once.

The client depends on no other repository. ``CLIENT_DIR`` is its own checkout, its ``.env`` sits at
that root (see ``.env.sample``) and its logs go to ``logs/`` there. The project a command works on
is the directory it is run from.
"""

import os
from pathlib import Path

from dotenv import load_dotenv

CLIENT_DIR = Path(__file__).resolve().parents[2]
load_dotenv(CLIENT_DIR / ".env")  # values already in the environment win
LOG_DIR = CLIENT_DIR / "logs"
FORCECOMPACT = CLIENT_DIR / "rust" / "forcecompact" / "target" / "release" / "forcecompact"


def env_limit(name: str, default: int) -> int:
    """An integer limit from the environment, where BLANK means unset rather than a parse error.

    ``.env.sample`` is meant to be copied as it ships and edited, and blanking a limit is how an
    operator goes back to its default, so a blank value is a normal thing to find in that file.
    Reading one as ``int("")`` raised at import time, which took every verb down with it. Only blank
    is forgiven: a value that is present and not a number still fails loudly, being a typo to fix.
    """
    raw = os.environ.get(name, "").strip()
    return default if not raw else int(raw)
