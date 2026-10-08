"""Stdlib regressions for credential-safe smoke diagnostics."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("office_smoke", Path(__file__).with_name("check-office.py"))
assert spec is not None and spec.loader is not None
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class SafeReportingTests(unittest.TestCase):
    def test_transport_errors_never_include_request_headers(self):
        error = RuntimeError("API request failed\nCookie: fixture-only-value\nAuthorization: fixture-only-value")
        self.assertEqual(smoke.safe_error_description(error), "RuntimeError")

    def test_layout_assertions_keep_their_actionable_dimensions(self):
        error = AssertionError({"width": 320, "bodyScrollWidth": 324})
        self.assertEqual(smoke.safe_error_description(error), str(error))

    def test_empty_assertions_report_a_safe_source_location(self):
        def fail_without_message():
            assert False

        try:
            fail_without_message()
        except AssertionError as error:
            description = smoke.safe_error_description(error)
        self.assertRegex(description, r"^AssertionError at test-reporting\.py:\d+$")


if __name__ == "__main__":
    unittest.main()
