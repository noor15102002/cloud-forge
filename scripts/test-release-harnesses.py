#!/usr/bin/env python3
"""Pure release fault and cleanup observer checks; no runtime application execution."""
import importlib.util
import copy
import json
from pathlib import Path
import unittest


def module(name, file):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(file))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


cleanup = module("cleanup", "pilot-cleanup-check.py")
operational = module("operational", "pilot-operational.py")


class ReleaseObserverTests(unittest.TestCase):
    def test_complete_empty_inventory_is_supported(self):
        for kind in ("container", "network", "volume", "image"):
            self.assertEqual(cleanup.records("", kind), [])

    def test_partial_and_malformed_inventory_rejected(self):
        for payload in ("a" * 64, "a" * 64 + " k3d-cloudforge-a", "short k3d-cloudforge-a\n", "a" * 64 + " one extra\n"):
            with self.subTest(payload=payload), self.assertRaises(ValueError):
                cleanup.records(payload, "container")

    def test_complete_resource_identity_retained(self):
        self.assertEqual(cleanup.records("a" * 64 + " k3d-cloudforge-a\n", "container"), ["k3d-cloudforge-a"])
        self.assertEqual(cleanup.records("buildx_buildkit_cloudforge-a0_state\n", "volume"), ["buildx_buildkit_cloudforge-a0_state"])

    def test_zero_and_findings_scan_envelopes_bind_expected_subject(self):
        for case in ("scanner-zero", "scanner-findings"):
            value = json.loads(operational.trivy_observation(case, "fixture:a", "sha256:" + "a" * 64))
            self.assertEqual(value["SchemaVersion"], 2)
            self.assertEqual(value["ArtifactName"], "fixture:a")
            self.assertEqual(value["Metadata"]["ImageID"], "sha256:" + "a" * 64)
            self.assertEqual(bool(value["Results"]), case == "scanner-findings")

    def test_invalid_scan_gate_rejects_synthesized_zero_or_lost_cleanup(self):
        report = {"status": "error", "evidence": [
            {"experiment_id": "container-build", "status": "pass"},
            {"experiment_id": "container-scan", "status": "error"},
            {"experiment_id": "environment-cleanup", "status": "pass"}],
            "diagnostics": [{"code": "scan_failed"}]}
        operational.qualify(report, 2, "scanner-empty")
        for mutation in ("synthesized-zero", "cleanup-error", "lost-build", "wrong-exit"):
            value = copy.deepcopy(report)
            if mutation == "synthesized-zero":
                value["evidence"][1]["measurements"] = [{"name": "vulnerabilities", "value": "0"}]
            elif mutation == "cleanup-error":
                value["evidence"][2]["status"] = "error"
            elif mutation == "lost-build":
                value["evidence"][0]["status"] = "error"
            with self.subTest(mutation=mutation), self.assertRaises(AssertionError):
                operational.qualify(value, 0 if mutation == "wrong-exit" else 2, "scanner-empty")

    def test_case_alias_fixture_retains_real_findings_before_the_alias(self):
        value = json.loads(operational.trivy_observation("scanner-case-alias", "fixture:a", "sha256:" + "a" * 64))
        self.assertEqual(len(value["Results"][0]["Vulnerabilities"]), 1)
        self.assertEqual(value["results"], [])
        self.assertLess(list(value).index("Results"), list(value).index("results"))

    def test_cleanup_gate_requires_original_failure_and_cleanup_error(self):
        report = {"status": "error", "evidence": [
            {"experiment_id": "rolling-deployment", "status": "fail"},
            {"experiment_id": "environment-cleanup", "status": "error"}],
            "diagnostics": [{"code": "cluster_remnant_cleanup_failed"}]}
        operational.qualify(report, 2, "cleanup-inventory")
        for index in (0, 1):
            value = copy.deepcopy(report)
            value["evidence"][index]["status"] = "pass"
            with self.assertRaises(AssertionError):
                operational.qualify(value, 2, "cleanup-inventory")

    def test_stable_startup_failure_requires_fail_unknown_cause_and_cleanup(self):
        report = {"status": "fail", "evidence": [
            {"experiment_id": "container-build", "status": "pass"},
            {"experiment_id": "deployment-readiness", "status": "fail", "measurements": [
                {"name": "ready_pods", "value": "0"}, {"name": "total_pods", "value": "2"}]},
            {"experiment_id": "environment-cleanup", "status": "pass"}],
            "diagnostics": [{"code": "readiness_failed", "status": "fail",
                "message": "Deployment did not become ready. Ready replicas: 0/2; restarts: 0. Root cause: not established."}]}
        operational.qualify(report, 1, "startup-failure")
        for mutation in ("operational-error", "wrong-exit", "cleanup-error", "ready-pod", "invented-cause", "new-lifecycle"):
            value = copy.deepcopy(report)
            if mutation == "operational-error":
                value["status"] = "error"
            elif mutation == "cleanup-error":
                value["evidence"][2]["status"] = "error"
            elif mutation == "ready-pod":
                value["evidence"][1]["measurements"][0]["value"] = "1"
            elif mutation == "invented-cause":
                value["diagnostics"][0]["message"] += " OOMKilled"
            elif mutation == "new-lifecycle":
                value["evidence"].append({"experiment_id": "pod-recovery", "execution": {"executed": True}})
            with self.subTest(mutation=mutation), self.assertRaises(AssertionError):
                operational.qualify(value, 2 if mutation == "wrong-exit" else 1, "startup-failure")

    def test_post_startup_node_health_requires_observed_conditions(self):
        nodes = {"items": [{"metadata": {"name": "omitted-private-node"}, "status": {"conditions": [
            {"type": "Ready", "status": "True"}, {"type": "DiskPressure", "status": "False"},
            {"type": "MemoryPressure", "status": "False"}, {"type": "PIDPressure", "status": "False"}]}}]}
        observed = operational.node_health_observation(json.dumps(nodes))
        self.assertTrue(observed["available"] and observed["healthy"] and observed["observed_after_failed_startup"])
        self.assertNotIn("omitted-private-node", json.dumps(observed))
        nodes["items"][0]["status"]["conditions"][1]["status"] = "True"
        self.assertFalse(operational.node_health_observation(json.dumps(nodes))["healthy"])
        nodes["items"][0]["status"]["conditions"].pop()
        with self.assertRaises(ValueError):
            operational.node_health_observation(json.dumps(nodes))


if __name__ == "__main__":
    unittest.main()
