import importlib.util
import pathlib
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("pplx_external.py")
SPEC = importlib.util.spec_from_file_location("pplx_external", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FetchFailureTest(unittest.TestCase):
    def test_rejects_unretrievable_content(self) -> None:
        response = {
            "output": [{"content": "Unable to retrieve content from the provided URL."}]
        }

        with self.assertRaisesRegex(MODULE.ToolError, "could not retrieve usable content"):
            MODULE.extract_fetched_content(response, "https://example.com/source")


if __name__ == "__main__":
    unittest.main()
