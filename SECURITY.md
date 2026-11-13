# Security Policy

## Supported Versions

| Version | Supported |
| ------- | --------- |
| 0.1.x   | yes       |

## Reporting a Vulnerability

Report vulnerabilities privately via GitHub's "Report a vulnerability" button
on the Security tab of this repository. Do not open a public issue.

deploydelta downloads and runs a Chrome build from the official Chrome for
Testing endpoint and renders arbitrary HTML with it. If you find a way for
rendered HTML to escape that boundary (file system access, unexpected network
egress beyond page resources, flag injection through Options), that is a
security issue: please report it.
