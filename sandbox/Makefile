# The ynf sandbox: private GitHub repositories rebuilt from this folder. eyelock/ynf-sandbox, from
# seed/, holds the fixtures; eyelock/ynf-sandbox-factory, from factory-seed/, is the factory's
# configuration repository, which enrols the sandbox and gives it default lanes (ADR-006).
#   make up      create it (or bring it in line with this folder)
#   make reset   delete it and create it again from scratch
#   make down    delete it
#   make calibrate  prove every fixture can still tell a fixed state from an unfixed one (tests
#                   the fixtures, not the factory; needs ynh with .agents/harness support,
#                   YNH=/path/to/ynh to choose one)
#   make e2e        rebuild the sandbox, run ynf against it until everything settles, and check
#                   each fixture's expect block (tests the factory; LANES=gofmt,deps,reclaim by default)
#   make e2e-only   the same, without the rebuild
#   make reset also empties the sandbox's ynm namespace (factory/github.com/eyelock/ynf-sandbox), so every
#   e2e run starts from the same memory; e2e checks each item that ran left its memories
#   make agent-image  build ynf-sandbox-agent:latest, the base the ynh lanes' harness images use
#                   (YNH_SRC=path to a ynh checkout; its origin/develop is built into the image)
# Deleting needs the delete_repo scope: gh auth refresh -s delete_repo
# State is in s3://ynf-terraform-state.eyelock.net/sandbox/, through the ynf-terraform AWS profile
# (infra/terraform-state).

TF := terraform -chdir=terraform
export GITHUB_TOKEN ?= $(shell gh auth token)
export AWS_PROFILE ?= ynf-terraform

.PHONY: init validate up down reset plan calibrate e2e e2e-only agent-image

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
	cd e2e && go run . -forget-memory

calibrate:
	cd calibrate && go run . -root ..

LANES ?= gofmt,deps,reclaim

e2e: reset e2e-only

e2e-only:
	cd e2e && go run . -root .. -lanes $(LANES)

YNH_SRC ?= ../../ynh
AGENT_IMAGE ?= ynf-sandbox-agent:latest

agent-image:
	@set -e; arch=$$(docker version --format '{{.Server.Arch}}'); ctx=$$(mktemp -d); \
	trap 'rm -rf "$$ctx"; git -C $(YNH_SRC) worktree remove --force "$$ctx/src" 2>/dev/null || true' EXIT; \
	cp images/agent/Dockerfile "$$ctx/"; mkdir -p "$$ctx/ynh"; \
	git -C $(YNH_SRC) fetch -q origin develop; \
	git -C $(YNH_SRC) worktree add -q --detach "$$ctx/src" origin/develop; \
	(cd "$$ctx/src" && CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -o "$$ctx/ynh/ynh" ./cmd/ynh); \
	docker build -q -t $(AGENT_IMAGE) "$$ctx"
