"""Where the repository's files are, resolved once.

The client is installed editable from ``<repo>/agenthub_client``, so the repository root is three
levels above this file. ``AGENTHUB_REPO_ROOT`` overrides it for a client installed anywhere else.
"""

import os
from pathlib import Path

from dotenv import load_dotenv

REPO_ROOT = Path(os.environ.get("AGENTHUB_REPO_ROOT") or Path(__file__).resolve().parents[3])
load_dotenv(REPO_ROOT / ".env")  # values already in the environment win
CLIENT_DIR = REPO_ROOT / "agenthub_client"
LOG_DIR = REPO_ROOT / "logs"
GO_DIR = REPO_ROOT / "agenthub_go"
FORCECOMPACT = CLIENT_DIR / "rust" / "forcecompact" / "target" / "release" / "forcecompact"
HOOKS_DIR = REPO_ROOT / ".claude" / "hooks"
TEAM_DIR = REPO_ROOT / "scripts" / "team" / "4genthub"
SKILL_INVENTORY = REPO_ROOT / "ai_docs" / "agent-system" / "skill-library.json"
