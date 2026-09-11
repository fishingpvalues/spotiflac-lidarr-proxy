# Security Policy

## Supported versions

Only the latest `0.x` release is supported. This project is pre-1.0 and the
HTTP surface is shaped by whatever Lidarr happens to call, so there are no
backports.

## Reporting a vulnerability

Report privately through GitHub's
[security advisories](https://github.com/fishingpvalues/spotiflac-lidarr-proxy/security/advisories/new).
Please do not open a public issue for anything exploitable.

Expect an acknowledgement within a week. This is a spare-time project, not a
product with an on-call rota - if that is not fast enough for your situation,
treat the service as untrusted and firewall it accordingly.

## Threat model, stated plainly

This is a homelab service. It assumes:

- **It is not exposed to the internet.** `SPF_API_KEY` is a shared secret in
  a query string, which Lidarr sends in the clear over HTTP on your own
  network. That is fine between two containers on a private bridge and is not
  fine on a public address. There is no TLS, no rate limiting and no account
  system, and none is planned - put it behind a reverse proxy or a VPN if you
  need any of that.
- **Anything it can reach, it is allowed to reach.** It shells out to
  `spotiflac-cli` and, in the full image, drives a real Chromium. A compromise
  of an upstream provider or mirror is a compromise of this container.
- **Its outbound address is visible to third parties.** Every backend talks to
  services that log the connecting IP. See the VPN section in the README.

Things that would be genuine vulnerabilities and are in scope: authentication
bypass on the API, command injection through a search term or category, path
traversal out of `SPF_OUTPUT_DIR`, or a secret written to logs.
