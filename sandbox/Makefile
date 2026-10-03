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

up: init
	$(TF) apply -auto-approve
	$(TF) output

down: init
	$(TF) destroy -auto-approve

reset: down up
