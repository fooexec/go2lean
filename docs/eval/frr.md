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

## The pipeline on FRR's advisories (2026-11-25)

`go2lean mine -osv-package Debian:frr` gave 55 advisories. For 30 of them
the tool found the real fix: a commit or pull request the advisory names
that changes source, not the release a CVE record's GIT range often
points to. 12 of those still reverse onto today's code, so `calibrate
-at-head` can check a spec on today's code with the fix reversed. The
other 18 sit in code that has changed since, and calibrating them needs
a configured build of each old revision.

`propose` drafted 13 candidates from the 12, seeing each fix's diff, and
the agent wrote specs for the nine on bgpd's parsers (the three on
`bgp_attr.c`: [specs_bgp_attr.c.txt](frr/specs_bgp_attr.c.txt)). They need bgpd's
own set-up (the BGP master, a bgp instance, a peer with an input stream),
taken from FRR's unit tests (`tests/bgpd/test_mp_attr.c`) and given to
the agent as guidance. libFuzzer runs them against the real code, linked
with `libbgp.a` and `libfrr`.

| advisory | fix | spec | today's code, fix reversed | today's code |
|---|---|---|---|---|
| CVE-2026-129456, use after free of Tunnel Encapsulation sub-TLVs | `5c241056` | interning keeps the sub-TLV chain owned while a route holds it | violated | holds (5.4 million fuzzer runs) |
| CVE-2026-37458, martian next hop accepted in MP_REACH_NLRI | `8102a8ae` | an IPv4 next hop in MP_REACH_NLRI gets the NEXT_HOP attribute's martian check | violated | holds (9.7 million fuzzer runs) |
| CVE-2026-37459, NHC TLV length | `693a2e02` | each TLV with its header fits in the attribute | | violated: the spec's error |
| CVE-2023-38406, FlowSpec NLRI length | `b1867d42` | a FlowSpec NLRI whose declared length passes the buffer is rejected, nothing read past it | violated | holds (5.1 million fuzzer runs) |
| CVE-2023-41360, ROUTE-REFRESH ORF | `c2b0d6a0` | the ORF entries are read within the message (two specs) | violated | holds (5.2 and 6.0 million fuzzer runs) |
| CVE-2023-41361, Software Version capability | `ff4c767a` | the version string is copied within its buffer, and the read pointer moves by the declared length | violated | violated: the spec's error |
| CVE-2026-37460, EVPN Type-2 length | `7676cad6` | a Type-2 NLRI is accepted only if its length matches its fields | violated | holds (4.7 million fuzzer runs) |
| CVE-2026-37460, EVPN Type-3 length | `7676cad6` | the same for Type-3 (13 or 25 bytes) | violated | holds (6.7 million fuzzer runs) |
| CVE-2026-37460, EVPN NLRI walk | `7676cad6` | an inconsistent length in any EVPN NLRI is rejected, nothing read past it | violated | holds (3.4 million fuzzer runs) |

Seven of the nine candidates calibrate; the other two are specs that are
wrong, and calibration rejected both.

CVE-2026-37458 is a semantic rule, not a memory error: a spec that
returns 0 when a martian next hop is accepted catches it, where
AddressSanitizer alone sees nothing.

The NHC spec is wrong, and calibration rejected it. It takes an attribute
of the 8-byte fixed part and no TLV as valid; `bgp_attr_nhc` requires
`BGP_NHC_MIN_LEN`, 12 bytes, and says so in its log ("length is too
short: 8, expected minimum 12"). The fix's diff does not show that
constant. A repair round that gives the agent the violation and the log,
and keeps the revised spec only if it is still violated before the fix,
would close this (#339).

The Software Version spec makes the same kind of error the other way: it
also feeds the parser a stream shorter than the capability's declared
length, which `bgp_capability_parse`, its only caller, never does (it
checks `start + caphdr.length > end` first). FRR's `stream_get` then
stops on its own assertion. Both errors are a caller's guarantee the
spec did not keep; the repair round would show the agent the caller.

Two judging errors came up, both fixed in the tool. The ROUTE-REFRESH
spec counted as spurious after the fix: the model, which gets bgpd's
set-up wrong, reported a nil dereference in it that the real code did
not reproduce, though the fuzzer ran clean. Its outcome is now a hold.
The Software Version crash, an assertion with no sanitizer report, was
inconclusive and counted as spurious too; a failed assertion is now a
crash the replay confirms, and an inconclusive replay counts as unknown.

What it took, each now in the tool:
- the real fix of an advisory, and its diff in the evidence (#336, #337);
- calibration on today's code with the fix reversed, since a 2024 fix's
  own revision no longer builds against today's headers (#336);
- the project's runtime linked into the model and the fuzzer (`-lib`,
  `-ldflags`), and a stub for `bgpd_privs`, defined in `bgp_main.c`;
- the project's own test set-up as the agent's guidance;
- a clean fuzzer run counted as a hold: the model gets stuck in bgpd's
  set-up on every trial, and the fuzzer is what checks these specs (#342).

## Next

- Calibrate the 18 advisories whose fix no longer reverses onto today's
  code: each needs a configured build of its own revision.
- `propose` and the agent on FRR's evidence, for the parsers no advisory
  covers yet.
- A binary line: FRR as distributions and vendors ship it (CVE-2023-38802
  also affected Pica8 PICOS), through an AArch64 semantics in Lean
  (Google DeepMind's AlphaProof Code, Lean 4.35.0 like go2lean's).
