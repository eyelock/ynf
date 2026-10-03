# The ynf sandbox: a private GitHub repository rebuilt from this folder.
#   make up      create it (or bring it in line with this folder)
#   make reset   delete it and create it again from scratch
#   make down    delete it
#   make calibrate  prove every fixture can still tell a fixed state from an unfixed one (tests
#                   the fixtures, not the factory; needs ynh with .agents/harness support,
#                   YNH=/path/to/ynh to choose one)
#   make e2e        rebuild the sandbox, run ynf against it until everything settles, and check
#                   each fixture's expect block (tests the factory; LANES=gofmt,deps by default)
#   make e2e-only   the same, without the rebuild
# Deleting needs the delete_repo scope: gh auth refresh -s delete_repo
# State is in s3://ynf-terraform-state.eyelock.net/sandbox/, through the ynf-terraform AWS profile
# (infra/terraform-state).

TF := terraform -chdir=terraform
export GITHUB_TOKEN ?= $(shell gh auth token)
export AWS_PROFILE ?= ynf-terraform

.PHONY: init validate up down reset plan calibrate e2e e2e-only

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

calibrate:
	cd calibrate && go run . -root ..

LANES ?= gofmt,deps

e2e: reset e2e-only

e2e-only:
	cd e2e && go run . -root .. -lanes $(LANES)
