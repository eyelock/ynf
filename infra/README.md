# Infra

What ynf's own development needs outside the code: where Terraform keeps its state, and the
GitHub repository's settings. The disposable test repository is not here; it has its own folder,
[`sandbox/`](../sandbox/README.md), and keeps its state in the same bucket.

| Path | What it is |
|---|---|
| [`terraform-state/`](terraform-state/README.md) | Terraform for the S3 bucket that holds the Terraform state for everything in this repository, and the IAM user that runs it. Applied once to bootstrap, then only to change the bucket or the user. |
| [`github/`](github/README.md) | Terraform for the `eyelock/ynf` repository: settings, branch protection, labels, Actions permissions. |
