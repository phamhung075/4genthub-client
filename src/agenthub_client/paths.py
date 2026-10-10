"""Where the client keeps its own files, resolved once.

The client depends on no other repository. ``CLIENT_DIR`` is its own checkout, its ``.env`` sits at
that root (see ``.env.sample``) and its logs go to ``logs/`` there. The project a command works on
is the directory it is run from.
"""

from pathlib import Path

from dotenv import load_dotenv

CLIENT_DIR = Path(__file__).resolve().parents[2]
load_dotenv(CLIENT_DIR / ".env")  # values already in the environment win
LOG_DIR = CLIENT_DIR / "logs"
FORCECOMPACT = CLIENT_DIR / "rust" / "forcecompact" / "target" / "release" / "forcecompact"
