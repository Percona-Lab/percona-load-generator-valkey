# Security policy

## Reporting a vulnerability

Please do not open a public issue. Use GitHub's **private vulnerability reporting** (the repository's
Security tab, "Report a vulnerability"). Include the plgv version (`plgv version`), what you did and what
you saw. You will get an answer, and a fix or a decision, as soon as the maintainers can; there is no
service-level promise.

## Supported versions

The latest release receives fixes.

## What plgv protects, and what it does not

See `docs/security-tls-acl.md`: credentials come from the environment or a mounted file and never reach an
output; connections can use TLS and mutual TLS; the web UI binds to loopback, checks the `Host` header and
`Origin`, and has no login. A recording holds your data in clear text. Dependencies are scanned with
`govulncheck` on every push.
