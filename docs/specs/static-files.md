# Static files stay under their root and behind their guards

## Applies to

General: any handler that maps a request path to a file (static routes,
file servers, `embed` or `fs.FS` handlers), and the router in front of it.

## Property

- A static-file handler serves only files under its configured root.
- A request reaches a file only through the routes and filters the
  application put in front of it. If the router or a filter treats a path
  as protected (a prefix with a guard on it), no other spelling of that
  path reaches the same file without the guard.

## Threat model

- The attacker chooses the request line: `..`, `./`, `//`, `\`,
  percent-encoding of any byte (`%2e`, `%2f`, `%5c`, `%61`), double
  encoding (`%252e`), mixed case, a trailing `/`, `;` parameters and NUL.
- The attacker does not control the configuration (the roots, the routes,
  the filters) or the files on disk.

## Oracle

Build a small file tree in a temporary directory, with a secret file
outside the root. The property holds when:
- no response ever contains the outside file;
- a file under a guarded prefix is served only when the guard ran.

The guard records that it ran, and the spec checks that record.

## Inputs

- A static route and a guarded prefix, configured as the package
  documents.
- Paths: built from the small pieces above, raw and as the router decodes
  them.

## Pitfalls

- A reference matcher that assumes a parameter stops at a slash was the
  spec's mistake on echo: the router documents otherwise.
- The router and the file handler may decode the path differently. Check
  what reaches the file, not what the router matched.

## Source

labstack/echo GHSA-vfp3-v2rc-x9fp (a `%2F`-encoded path matched no
protected route but was served) and its successor (any percent-encoded
character); beego GHSA-95f9-94vc-665h (path traversal).
