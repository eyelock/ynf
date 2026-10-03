# The ynf sandbox: a private GitHub repository rebuilt from this folder.
#   make up      create it (or bring it in line with this folder)
#   make reset   delete it and create it again from scratch
#   make down    delete it
# Deleting needs the delete_repo scope: gh auth refresh -s delete_repo
# State is in s3://ynf-terraform-state.eyelock.net/sandbox/, through the ynf-terraform AWS profile
# (infra/terraform-state).

TF := terraform -chdir=terraform
export GITHUB_TOKEN ?= $(shell gh auth token)
export AWS_PROFILE ?= ynf-terraform

.PHONY: init validate up down reset plan

init:
	$(TF) init -upgrade

validate: init
	$(TF) validate

plan: init
	$(TF) plan

# GitHub sometimes answers "403 Repository has been locked" for a few seconds after a new
# repository's first push. Apply is idempotent, so a failed apply is retried once.
up: init
	$(TF) apply -auto-approve || { echo "apply failed; retrying once"; sleep 15; $(TF) apply -auto-approve; }
	$(TF) output

down: init
	$(TF) destroy -auto-approve

reset: down up
