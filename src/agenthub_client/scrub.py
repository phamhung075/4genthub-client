"""Secret scrubber for text the OpenRig bridge sends to 4genthub.

``scrub`` redacts known secret values (exact, base64 and URL-encoded forms) and
well-known secret patterns, then truncates to ``MAX_LEN``. It fails closed: any
internal error returns ``("", True)`` so nothing unscrubbed leaves the machine.
The patterns mirror the server-side scanner and share its fixture
(agenthub_go/fastmcp/seat_management/domain/secretscan/testdata/scan_cases.json).
"""

import base64
import re
import urllib.parse
from collections.abc import Mapping

MAX_LEN = 200
MIN_SECRET_LEN = 6

# Go RE2 `\s` is exactly [\t\n\f\r ]; Python's is Unicode. These spell the Go class out so both
# scanners split text at the same characters (shared fixture: secretscan/testdata/scan_cases.json).
_WS = r"[\t\n\f\r ]"
_NOT_WS = r"[^\t\n\f\r ]"

_SECRET_NAME_RE = re.compile(r"TOKEN|SECRET|KEY|PASSWORD|PASSWD|CREDENTIAL", re.IGNORECASE)

_PATTERNS: list[tuple[str, re.Pattern[str]]] = [
    ("pem", re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|\Z)", re.DOTALL)),
    ("jwt", re.compile(r"eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+")),
    ("bearer", re.compile(r"Bearer" + _WS + r"+[A-Za-z0-9._~+/=-]{20,}", re.IGNORECASE)),
    ("sk", re.compile(r"sk-[A-Za-z0-9_-]{20,}")),
    ("aws", re.compile(r"AKIA[0-9A-Z]{16}")),
    ("github", re.compile(r"gh[pousr]_[A-Za-z0-9]{30,}")),
    ("url-credentials", re.compile(r"://[^\t\n\f\r /:@]*:[^\t\n\f\r /]+@")),
]

_PAIR_RE = re.compile(
    r"((?:password|passwd|secret|token|api[_-]?key)" + _WS + r"*[=:]" + _WS + r"*)(?!\[REDACTED)(" + _NOT_WS + r"{6,})",
    re.IGNORECASE,
)


def secret_values_from_env(environ: Mapping[str, str]) -> dict[str, str]:
    """Map secret-looking env var names to their values (values under 6 chars ignored)."""
    return {
        name: value
        for name, value in environ.items()
        if _SECRET_NAME_RE.search(name) and len(value) >= MIN_SECRET_LEN
    }


def _forms(value: str) -> set[str]:
    raw = value.encode("utf-8")
    return {
        value,
        base64.b64encode(raw).decode("ascii"),
        base64.urlsafe_b64encode(raw).decode("ascii"),
        urllib.parse.quote(value, safe=""),
    }


def scrub(text: str, secret_values: Mapping[str, str]) -> tuple[str, bool]:
    """Return ``(clean_text, redacted)``; ``("", True)`` on any failure."""
    try:
        clean = text
        for name, value in sorted(secret_values.items(), key=lambda kv: -len(kv[1])):
            if len(value) < MIN_SECRET_LEN:
                continue
            for form in sorted(_forms(value), key=len, reverse=True):
                clean = clean.replace(form, f"[REDACTED:{name}]")
        for label, pattern in _PATTERNS:
            clean = pattern.sub(f"[REDACTED:{label}]", clean)
        clean = _PAIR_RE.sub(lambda m: m.group(1) + "[REDACTED:value]", clean)
        return clean[:MAX_LEN], clean != text
    except Exception:
        return "", True
