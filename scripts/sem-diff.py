#!/usr/bin/env python3
"""Each Goose semantics test whose model outcome is not a clean exhausted,
one line: the test, the outcome, and whether it read havoc or assumed."""
import glob
import json
import sys

ws = sys.argv[1]
prefix = "p_github_com_fooexec_go2lean_testdata_goosesem_seq"
proofs = {}
for f in glob.glob(f"{ws}/proofs/*.json"):
    p = json.load(open(f))
    proofs[p["property"]] = p
verdicts = {}
for f in glob.glob(f"{ws}/verdicts/*.json"):
    v = json.load(open(f))
    cx = json.load(open(f"{ws}/counterexamples/{v['counterexample']}.json"))
    verdicts[cx["property"]] = (v, cx)
for f in sorted(glob.glob(f"{ws}/properties/*.json")):
    pid = json.load(open(f))["id"]
    if not pid.startswith(prefix) or not pid.endswith("_sequence"):
        continue
    name = pid[len(prefix):-len("_sequence")]
    if pid in verdicts:
        v, cx = verdicts[pid]
        why = (v.get("diagnosis") or {}).get("reason", "")
        tags = [t for t, on in (("havoc", cx.get("havoc")), ("assuming", cx.get("assumptions"))) if on]
        print(" ".join([name, v["status"], why] + tags))
    elif pid in proofs:
        if proofs[pid]["status"] != "exhausted":
            print(name, proofs[pid]["status"])
    else:
        print(name, "no_result")
