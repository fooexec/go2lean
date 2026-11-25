# FRRouting (FRR): the C line on a routing suite

[FRR](https://github.com/FRRouting/frr) is the routing protocol suite
(BGP, OSPF, IS-IS, BABEL, ...) of most open-source and many commercial
routers. Its daemons parse messages from peers, and nearly all of its 55
CVEs on OSV are in those parsers: BGP UPDATE and OPEN messages, path
attributes, FlowSpec, OSPF Traffic Engineering and opaque LSAs, babeld
hellos. It is the second target of the mined-properties pipeline (#315),
after [quic-go](quic-go.md), and the first in C.

## Setup (2026-11-25)

FRR master `92ac4f532`, built with clang 18 (`./configure` with libyang
2.1.128 built from source; Ubuntu 24.04 ships 2.1.30). A spec includes
the FRR file it checks, so its static functions are visible, with FRR's
compile flags (`-DHAVE_CONFIG_H -I. -Iinclude -Ilib -Ilib/assert
-fms-extensions`), and the replay and the fuzzer link `libfrr`:

```sh
go2lean llvm -memory -specs-only -maxlen 32 -fuzz 60s \
  -cflags "-DHAVE_CONFIG_H,-I$F,-I$F/include,-I$F/lib,-I$F/lib/assert,-I$F/bgpd,-fms-extensions" \
  -ldflags "-L$F/lib/.libs -lfrr -Wl,-rpath,$F/lib/.libs" \
  $F/bgpd/zz_spec_flowspec.c
```

Three things the C line lacked, added for this:
- `-ldflags`, for the links of the replay and the fuzzer;
- read-only binds of the directories `-ldflags` names in the sandbox, which
  hides `/tmp` and the home;
- `-maxlen`: generated buffers were at most 8 bytes, too short for a
  message with more than a few fields.

## Mining

`go2lean mine -osv-package Debian:frr` (C support added for FRR): 55
advisories, 693 fix commits, 377 RFC sections cited in the code, 69 pull
requests, 18 open issues, in 4.5 minutes over 46290 commits. OSV lists
FRR's advisories under its Debian package; each entry is followed to the
CVE record it tracks, whose GIT range names the fix commit for 19 of
them.

## Calibration: CVE-2026-37457 (FlowSpec operator array)

`bgp_flowspec_op_decode` and `bgp_flowspec_bitmask_decode` decode the
operator list of a FlowSpec component (RFC 8955, Section 4.2.1.1) into
an array of `BGP_PBR_MATCH_VAL_MAX` (5) entries. Before `0e6882bc72c0`
the bound check was `loop > BGP_PBR_MATCH_VAL_MAX`, so a sixth chained
operator was written past the array. The spec
([spec_flowspec.c.txt](frr/spec_flowspec.c.txt)) decodes any buffer into
an array of 5 and lets AddressSanitizer judge the writes; it names no
input. The fix changes one file, so the two revisions were checked in
the configured tree, with the spec including that file before and after
the fix.

| revision | `bgp_flowspec_op_decode` | `bgp_flowspec_bitmask_decode` |
|---|---|---|
| `0e6882bc72c0^` | the model finds a store out of bounds (random search, a 16-byte component); the replay confirms a stack-buffer-overflow WRITE of 2 bytes | the same |
| `0e6882bc72c0` | no undefined behaviour in 2000 trials; libFuzzer: no crash in 30.7 million runs | no undefined behaviour in 2000 trials; libFuzzer: no crash in 32.3 million runs |

## Next

- Calibrate the other advisories with a fix commit (bgpd's attribute and
  NLRI parsers, ospfd's TE and opaque LSA parsers).
- `propose` and the agent on FRR's evidence, for the parsers no advisory
  covers yet.
- A binary line: FRR as distributions and vendors ship it (CVE-2023-38802
  also affected Pica8 PICOS), through an AArch64 semantics in Lean
  (Google DeepMind's AlphaProof Code, Lean 4.35.0 like go2lean's).
