# Redirects stay on the same site

## Applies to

General: any HTTP router or middleware that issues redirects on its own
(trailing slash, fixed or cleaned path, case, forwarded prefix).

## Property

A redirect the router or one of its middlewares issues by itself must stay
on the same site: whatever the request's path, raw path, query and
headers, the `Location` it sends, resolved by a browser against the
request URL, has the request's own scheme and host. Redirects the
application asks for explicitly, with a target it chose, are not covered.

## Threat model

- The attacker chooses the whole request line and every header: the
  `Host`, `X-Forwarded-Prefix`, `X-Forwarded-Host` and any other header
  the router reads, and a path with `//`, `\`, `/\`, `\/`, `/./`, `..`,
  `%2f`, `%5c`, `%09`, `@`, `:`, tabs and newlines.
- The attacker does not control the routes or the router's options.
- The victim's browser follows the `Location`.

## Oracle

Resolve the `Location` the way a browser does (WHATWG URL): `\` is `/`,
tabs and newlines are removed, a leading `//` is scheme-relative, and a
relative reference is resolved against the request URL. Write this
resolution in the spec; do not use the package's own path cleaning. The
property holds when the result's scheme and host are the request's.

## Inputs

- Routes: a few, with and without trailing slashes, and with a parameter
  as the first segment (`/:p/`), so that the attacker's chosen host is a
  path the router accepts.
- The router's redirect options, both on and off.
- Paths: built from the small pieces above, so the search can combine
  them.
- Headers: an attacker-chosen `Host` and forwarding headers, including
  values that a sanitizer may empty.

## Pitfalls

- A spec that compares the `Location` with the old version's exact format
  fails on every later version for the wrong reason. Check the same-site
  property only.
- A redirect needs the lookup to succeed first: without a parameter route
  the attacker's path is never redirected, and the property holds
  vacuously.

## Source

go-chi/chi GHSA-vrw8-fxc6-2r93 (the `Host` header in the redirect) and
GHSA-mqqf-5wvp-8fh8 (a backslash bypass that the first fix introduced).
