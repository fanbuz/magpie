#!/usr/bin/env python3
"""Run the PR 239 review comparisons on synthetic data; never reads user sessions."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[3]
BASELINE = "7d5612005aa72959918d25c2563e4becd23c74bd"
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--output", type=Path)
args = parser.parse_args()
OUT = args.output or Path(tempfile.mkdtemp(prefix="magpie-usage-ablation-"))
OUT = OUT.resolve()
OUT.mkdir(parents=True, exist_ok=True)


def overlay(name, replacements):
    mapping = {}
    for source, contents in replacements.items():
        dest = OUT / (name + "-" + Path(source).name)
        dest.write_text(contents)
        mapping[str(ROOT / source)] = str(dest)
    target = OUT / (name + ".json")
    target.write_text(json.dumps({"Replace": mapping}))
    return ["-overlay", str(target)]


def run(name, command, env=None, negative=False):
    print(name, flush=True)
    with (OUT / (name + ".log")).open("w") as log:
        result = subprocess.run(command, cwd=ROOT, env={**os.environ, **(env or {})}, stdout=log, stderr=subprocess.STDOUT)
    if negative:
        text = (OUT / (name + ".log")).read_text()
        if result.returncode != 1 or "Today waited on All's blocked file IO" not in text:
            raise SystemExit("Global-lock negative control did not fail as expected: " + str(OUT))
    elif result.returncode:
        raise SystemExit(name + " failed; see " + str(OUT))


replacements = {p: subprocess.check_output(["git", "show", BASELINE + ":" + p], cwd=ROOT, text=True)
                for p in ["internal/usage/request_page.go", "internal/sessions/calls.go"]}
replacements["internal/usage/request_snapshot.go"] = "package usage\n"
base_overlay = overlay("baseline", replacements)
scale = ["-tags", "nogui", "./internal/gui", "-run", "^TestUsageScale$", "-count=1", "-v"]
scale_env = {"MAGPIE_USAGE_SCALE": "1", "MAGPIE_USAGE_ACTIVE": "1"}
run("baseline", ["go", "test", *base_overlay, *scale], scale_env)
run("final", ["go", "test", *scale], scale_env)
# Restore just the duplicate parser retention to isolate its memory/cost tradeoff.
calls = (ROOT / "internal/sessions/calls.go").read_text()
start = calls.index("func ReadCallSource(s CallSource)")
retained = calls[:start] + '''func ReadCallSource(s CallSource) []Call {
 return readCalls(file{path:s.Path,key:s.Key,agent:s.Agent,size:s.Size,mod:s.Modified}).Calls
}
'''
run("without-parser-release", ["go", "test", *overlay("retained", {"internal/sessions/calls.go": retained}), *scale], scale_env)
run("matching", ["go", "test", "-tags", "nogui", "./internal/usage", "-run", "^$", "-bench", "^BenchmarkMatchedLocal$", "-benchtime=1x", "-count=3"])
run("fingerprint", ["go", "test", "-tags", "nogui", "./internal/sessions", "-run", "^TestSampledPrefixFingerprint$", "-bench", "^BenchmarkPrefixFingerprint$", "-benchtime=100x", "-count=3"])
run("snapshot", ["go", "test", "-tags", "nogui", "./internal/usage", "-run", "^TestPackedGatewayRetainsOversizedSnapshot$", "-bench", "^BenchmarkRequestSnapshot$", "-benchtime=10x", "-count=3"])
run("unlocked", ["go", "test", "-tags", "nogui", "./internal/usage", "-run", "^TestQueryPageIOOutsideCacheLock$", "-count=1", "-v"])
page = (ROOT / "internal/usage/request_page.go").read_text()
page = page.replace("var requestCache requestIndex", "var requestCache requestIndex\nvar ablationIO sync.Mutex")
needle = "readSource func(sessions.CallSource) []sessions.Call) RequestPage {"
if needle not in page:
    raise SystemExit("Query implementation changed; update the lock ablation")
page = page.replace(needle, needle + "\n ablationIO.Lock(); defer ablationIO.Unlock()")
run("without-unlocked-io", ["go", "test", *overlay("locked", {"internal/usage/request_page.go": page}), "-tags", "nogui", "./internal/usage", "-run", "^TestQueryPageIOOutsideCacheLock$", "-count=1", "-v"], negative=True)
print("Results:", OUT)
