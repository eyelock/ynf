# Security policy

## Reporting a vulnerability

Report vulnerabilities privately, through GitHub's private vulnerability reporting:

1. Open the [Security tab](https://github.com/eyelock/ynf/security) of the repository.
2. Choose **Report a vulnerability** and describe the problem: what is affected, how to reproduce
   it, and what an attacker could do with it.

Please do not open a public issue or discussion for a vulnerability, and do not include real
credentials in a report.

You can expect an acknowledgement within a few days. The maintainer will confirm the problem,
work on a fix, and agree a disclosure date with you before publishing an advisory.

## Supported versions

Fixes are made on the latest release. Older releases are not patched.

## Scope

ynf runs agents against repositories and holds tokens for the forges and trackers it talks to.
Reports about containment, token handling, and what a run can reach are in scope.
