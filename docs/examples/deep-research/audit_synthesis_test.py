import json
import tempfile
import unittest
from pathlib import Path

import audit_synthesis


class AuditTests(unittest.TestCase):
    def test_audit_accepts_builtin_uuid_artifact_id(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self._write_fixture(Path(directory))
            knowledge_path = Path(paths["knowledge_paths"][0])
            knowledge = json.loads(knowledge_path.read_text(encoding="utf-8"))
            knowledge["quotes"][0]["artifact_id"] = (
                "artifact-123e4567-e89b-12d3-a456-426614174000"
            )
            knowledge_path.write_text(json.dumps(knowledge), encoding="utf-8")

            result = self._audit(paths)

            self.assertTrue(result["pass"])
            self.assertEqual([], result["issues"])

    def test_audit_accepts_typed_tool_validated_artifact_id(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self._write_fixture(Path(directory))
            knowledge_path = Path(paths["knowledge_paths"][0])
            knowledge = json.loads(knowledge_path.read_text(encoding="utf-8"))
            knowledge["quotes"][0]["exact_quote"] = (
                "No artifact text comparison is needed."
            )
            knowledge_path.write_text(json.dumps(knowledge), encoding="utf-8")

            result = self._audit(paths)

            self.assertTrue(result["pass"])
            self.assertEqual([], result["issues"])
            self.assertEqual(1, result["match_modes"]["typed_tool_validated"])
            self.assertEqual(1, result["report_quote_ids"])
            self.assertEqual(1, result["knowledge_quote_ids"])
            full_audit = json.loads(
                Path(result["audit_path"]).read_text(encoding="utf-8")
            )
            self.assertEqual(
                "typed_tool_validated",
                full_audit["matches"][0]["match_mode"],
            )
            self.assertEqual("topic-quote-001", full_audit["matches"][0]["quote_id"])

    def test_audit_rejects_quote_without_trusted_reference(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self._write_fixture(Path(directory))
            knowledge_path = Path(paths["knowledge_paths"][0])
            knowledge = json.loads(knowledge_path.read_text(encoding="utf-8"))
            del knowledge["quotes"][0]["quote_ref"]
            knowledge_path.write_text(json.dumps(knowledge), encoding="utf-8")

            result = self._audit(paths)

            self.assertFalse(result["pass"])
            self.assertIn(
                "invalid_quote_ref",
                {issue["code"] for issue in result["issues"]},
            )

    def test_audit_reports_invented_unused_and_wrong_url_references(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self._write_fixture(
                Path(directory),
                body_quote_id="topic-quote-999",
                source_url="https://example.com/wrong",
            )

            result = self._audit(paths)

            self.assertFalse(result["pass"])
            codes = {issue["code"] for issue in result["issues"]}
            self.assertIn("invented_reference", codes)
            self.assertIn("unused_reference", codes)
            self.assertIn("wrong_url_mapping", codes)

    def test_audit_does_not_match_quotes_unused_by_final_report(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self._write_fixture(Path(directory))
            knowledge_path = Path(paths["knowledge_paths"][0])
            knowledge = json.loads(knowledge_path.read_text(encoding="utf-8"))
            knowledge["knowledge"].append(
                {
                    "id": "topic-kb-002",
                    "claim": "This claim is not used by the final report.",
                    "confidence": "medium",
                    "quote_ids": ["topic-quote-002"],
                }
            )
            knowledge["quotes"].append(
                {
                    "id": "topic-quote-002",
                    "quote_ref": "quote-ref-22222222222222222222222222222222",
                    "source_title": "Unused example",
                    "url": "https://example.com/unused",
                    "artifact_id": knowledge["quotes"][0]["artifact_id"],
                    "locator": "paragraph 2",
                    "exact_quote": "Text absent from the artifact.",
                }
            )
            knowledge_path.write_text(json.dumps(knowledge), encoding="utf-8")

            result = self._audit(paths)

            self.assertTrue(result["pass"])
            self.assertEqual(1, result["artifacts_checked"])
            self.assertEqual(2, result["knowledge_quote_ids"])

    def test_audit_rejects_artifacts_from_another_run(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            current = self._write_fixture(root / "current")
            foreign = self._write_fixture(root / "foreign")

            result = audit_synthesis.audit(
                foreign,
                workspace=Path(current["report_path"]).parent,
            )

            self.assertFalse(result["pass"])
            self.assertIn(
                "invalid_report_path",
                {issue["code"] for issue in result["issues"]},
            )

    def test_audit_rejects_report_without_citations(self):
        with tempfile.TemporaryDirectory() as directory:
            paths = self._write_fixture(Path(directory))
            Path(paths["report_path"]).write_text(
                "# Report\n\nA conclusion without evidence.\n",
                encoding="utf-8",
            )

            result = self._audit(paths)

            self.assertFalse(result["pass"])
            self.assertIn(
                "missing_citations",
                {issue["code"] for issue in result["issues"]},
            )

    def _audit(self, paths):
        return audit_synthesis.audit(
            paths,
            workspace=Path(paths["report_path"]).parent,
        )

    def _write_fixture(
        self,
        root: Path,
        body_quote_id: str = "topic-quote-001",
        source_url: str = "https://example.com/source",
    ):
        run = root / ".r42" / "runs" / "run-test" / "blocks"
        knowledge_dir = run / "knowledge" / "task"
        report_dir = run / "report"
        resolution_dir = run / "resolution"
        for path in (knowledge_dir, report_dir, resolution_dir):
            path.mkdir(parents=True, exist_ok=True)

        knowledge_path = knowledge_dir / "knowledge.json"
        knowledge_path.write_text(
            json.dumps(
                {
                    "artifact_path": str(knowledge_path),
                    "subquestion": "What happened?",
                    "knowledge": [
                        {
                            "id": "topic-kb-001",
                            "claim": "The exchange rate remained stable.",
                            "confidence": "high",
                            "quote_ids": ["topic-quote-001"],
                        }
                    ],
                    "quotes": [
                        {
                            "id": "topic-quote-001",
                            "quote_ref": "quote-ref-11111111111111111111111111111111",
                            "source_title": "Example",
                            "url": "https://example.com/source",
                            "artifact_id": "artifact-11111111111111111111111111111111",
                            "locator": "paragraph 1",
                            "exact_quote": "The exchange rate remained stable.",
                        }
                    ],
                }
            ),
            encoding="utf-8",
        )

        report_path = report_dir / "report.md"
        report_path.write_text(
            "# Report\n\n"
            f"The exchange rate remained stable. [{body_quote_id}]\n\n"
            "| Quote IDs | URL |\n"
            "| --- | --- |\n"
            f"| topic-quote-001 | {source_url} |\n",
            encoding="utf-8",
        )
        resolution_path = resolution_dir / "resolution.json"
        resolution_path.write_text(
            json.dumps({"conflicts": []}),
            encoding="utf-8",
        )
        return {
            "report_path": str(report_path),
            "knowledge_paths": [str(knowledge_path)],
            "resolution_path": str(resolution_path),
        }


if __name__ == "__main__":
    unittest.main()
