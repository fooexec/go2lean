# Two HTTP parsers read a message the same way

## Applies to

General: any Go package that parses HTTP/1.x requests or responses by
itself instead of calling `net/http` (servers and clients built on their
own parser, proxies, test servers, gateways).

## Property

For a raw HTTP/1.1 request, a package's parser and `net/http.ReadRequest`
must agree on the framing: where the message ends, the method, the target,
the `Host`, the `Content-Length` and the body length. When `net/http`
rejects the message (a malformed header, two different `Content-Length`
values, `Content-Length` with `Transfer-Encoding`, a bare line feed in a
header name or value), the package must reject it too, or read exactly the
same bytes as the message.

## Threat model

- Two servers in a chain (a proxy or gateway in front of the package, or
  the package in front of `net/http`) disagree on where a request ends. The
  attacker puts a second request in the bytes the first server counts as
  body, and the second server sees it as a new request (request smuggling).
- The attacker chooses every byte of the message.

## Oracle

`net/http.ReadRequest` over the same bytes. Compare:
- accepted or rejected;
- if both accept: method, request URI, `Host`, `Content-Length`, whether
  the body is chunked, and the number of bytes consumed.
Do not compare the error text, header order or the case of header names.

## Inputs

Built from small pieces: methods, a target (`/`, `*`, absolute URI),
versions (`HTTP/1.0`, `HTTP/1.1`), header lines with a space before the
colon, obs-fold continuation lines, bare `\n` line ends, duplicate
`Content-Length`, `Content-Length: +5` and `0x10`, `Transfer-Encoding:
chunked` with different spellings (`Chunked`, ` chunked`, `chunked, gzip`),
chunk sizes with extensions and with signs.

## Pitfalls

- A strict package that rejects more than `net/http` is not a defect. The
  property is violated only when the package accepts what `net/http`
  rejects, or reads another length.
- Compare bytes consumed, not only the parsed values: two parsers can agree
  on every field and still disagree on where the message ends.
- Call `net/http.ReadRequest` in the spec when the package's dependencies
  allow it, and compare what it returns. A reference re-written from the
  RFC is usually stricter than `net/http`, and the spec then fails on
  messages both parsers accept (the first run, `docs/eval/http-framing.md`).
