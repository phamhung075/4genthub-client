"""Tests for agenthub_client.scrub against the shared secret-scan fixture."""

import base64
import importlib.util
import json
import urllib.parse
from pathlib import Path

import pytest

pytestmark = pytest.mark.unit

ROOT = Path(__file__).resolve().parents[2]
MODULE_PATH = ROOT / "agenthub_client" / "src" / "agenthub_client" / "scrub.py"
FIXTURE = (
    ROOT
    / "agenthub_go/fastmcp/seat_management/domain/secretscan/testdata/scan_cases.json"
)


def _load_module():
    return importlib.import_module("agenthub_client.scrub")


scrubber = _load_module()
CASES = json.loads(FIXTURE.read_text(encoding="utf-8"))["cases"]

# The fake secret each has_secret case must lose.
SECRET_PARTS = {
    "jwt": "dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk",
    "bearer": "abcdef1234567890abcdef1234567890",
    "openai-style": "sk-abcdefghijklmnopqrstuvwxyz0123456789ABCD",
    "aws-access-key": "AKIAIOSFODNN7EXAMPLE",
    "github-token": "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
    "private-key": "MIIEfake",
    "password-pair": "hunter2hunter2",
    "secret-colon": "s3cr3tValue-9981",
    "token-equals": "abc123def456ghi789",
    "url-credentials": "s3cretpass",
    "url-at-in-password": "ssw0rd99",
    "url-empty-user": "onlypass",
    "url-nbsp-in-password": "pass\u00a0word123",
    "url-vtab-in-password": "pa\u000bss1234",
}


@pytest.mark.parametrize(
    "case", [c for c in CASES if c["has_secret"]], ids=lambda c: c["name"]
)
def test_fixture_secret_cases_are_redacted(case):
    clean, redacted = scrubber.scrub(case["text"], {})
    assert redacted is True
    assert clean != case["text"]
    assert SECRET_PARTS[case["name"]] not in clean


@pytest.mark.parametrize(
    "case", [c for c in CASES if not c["has_secret"]], ids=lambda c: c["name"]
)
def test_fixture_clean_cases_are_unchanged(case):
    assert scrubber.scrub(case["text"], {}) == (case["text"], False)


def test_pair_redacts_only_the_value():
    clean, _ = scrubber.scrub("db password=hunter2hunter2 ok", {})
    assert clean.startswith("db password=")
    assert clean.endswith(" ok")


def test_env_value_exact_base64_and_urlencoded_forms():
    value = "my secret/value+9"
    env = {"MY_API_KEY": value}
    for form in (
        value,
        base64.b64encode(value.encode()).decode(),
        base64.urlsafe_b64encode(value.encode()).decode(),
        urllib.parse.quote(value, safe=""),
    ):
        clean, redacted = scrubber.scrub(f"saw {form} here", env)
        assert redacted is True
        assert form not in clean
        assert "[REDACTED:MY_API_KEY]" in clean


def test_secret_values_from_env_filters_by_name_and_length():
    env = {
        "AGENTHUB_TOKEN": "tok-123456",
        "DB_PASSWORD": "pw1234",
        "SSH_PASSWD": "pw1234x",
        "API_KEY": "short",
        "HOME": "/home/someone",
        "MY_CREDENTIAL": "cred-abcdef",
        "CLIENT_SECRET": "s3cr3t",
    }
    assert scrubber.secret_values_from_env(env) == {
        "AGENTHUB_TOKEN": "tok-123456",
        "DB_PASSWORD": "pw1234",
        "SSH_PASSWD": "pw1234x",
        "MY_CREDENTIAL": "cred-abcdef",
        "CLIENT_SECRET": "s3cr3t",
    }


def test_truncates_after_redaction():
    secret = "Z9" * 10
    text = ("x" * 195) + secret
    clean, redacted = scrubber.scrub(text, {"TOKEN_A": secret})
    assert redacted is True
    assert len(clean) == scrubber.MAX_LEN
    assert secret not in clean


def test_truncation_alone_is_not_redaction():
    clean, redacted = scrubber.scrub("a" * 500, {})
    assert (len(clean), redacted) == (200, False)


def test_exception_fails_closed():
    assert scrubber.scrub(None, {}) == ("", True)
    assert scrubber.scrub("text", None) == ("", True)
