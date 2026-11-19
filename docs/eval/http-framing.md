# HTTP framing on fasthttp (2026-11-19)

The first run of the [http-framing](../specs/http-framing.md) property: a
hand-written request parser must read a message as `net/http.ReadRequest`
does. fasthttp (23k stars, `7b1ac76`) parses requests with its own code.

The agent was given only the catalog file, as `-guidance`. It wrote two
specs for `RequestHeader.parse`: a postcondition on one parse, and a
sequence spec that builds a request from smuggling-prone pieces (duplicate
or signed `Content-Length`, spellings of `Transfer-Encoding`, a space before
the colon, obs-fold, bare LF and CR).

## Result: no framing disagreement

- **Model search:** the sequence spec was violated at trial 154 (`GET /`,
  a header name with a space, `Host`). `net/http` accepts that message too,
  and frames it the same way: the agent had written its oracle from the RFC
  (a header name is a token), not from `net/http`, so the verdict is a spec
  stricter than the reference, the pitfall the catalog file names. It is
  not a defect.
- **Direct differential, by hand:** the same pieces, every sequence of one
  and two pieces (484 messages), parsed by `RequestHeader.parse` and
  `http.ReadRequest`. fasthttp rejects whatever `net/http` rejects, and
  frames every accepted message the same way (`Content-Length`, chunked or
  none), with one exception.
- **The exception:** `Transfer-Encoding: identity` makes `net/http` answer
  "unsupported transfer encoding". fasthttp ignores the header and frames
  by `Content-Length` (5 here) or none. A chain would need a front end that
  frames by `identity` in some third way; the header is obsolete (RFC 9112).
  Not reported.
- fasthttp's parser was hardened against smuggling in earlier releases
  (rejecting a repeated or signed `Content-Length`, a `Transfer-Encoding`
  other than `chunked`), which the run confirms.

## What this says about the method

- **A reference oracle must be the reference.** The agent re-wrote the
  reference from memory, and its version was stricter. The catalog entry
  now says to call `net/http.ReadRequest` in the spec when the package's
  dependencies allow it, and to compare what it returns.
- **A clean result on a hardened parser** is the same outcome as lcms2
  ([clean results](c-clean-results.md)): the property is right, the target
  has already been worked. The next targets for it are parsers that no one
  has differential-tested against `net/http`: other Go HTTP/1 servers and
  proxies built on their own parser.
