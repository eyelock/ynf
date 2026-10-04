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
#   make e2e-factory  the same with ynf inside the factory image, as a job runner would run it
#                   (ADR-009, shape B): the sandbox's harness built on ynf's factory image
#                   (make -C .. factory-image first; FACTORY_BASE names it)
#   make reset also empties the sandbox's ynm namespace (factory/github.com/eyelock/ynf-sandbox), so every
#   e2e run starts from the same memory; e2e checks each item that ran left its memories
#   make agent-image  build ynf-sandbox-agent:latest, the base the ynh lanes' harness images use,
#                   on the ynh release image YNH_BASE; YNH_SRC=path to a ynh checkout builds its
#                   origin/develop into the image instead, to try ynh before it is released
# Deleting needs the delete_repo scope: gh auth refresh -s delete_repo
# State is in s3://ynf-terraform-state.eyelock.net/sandbox/, through the ynf-terraform AWS profile
# (infra/terraform-state).

TF := terraform -chdir=terraform
export GITHUB_TOKEN ?= $(shell gh auth token)
export AWS_PROFILE ?= ynf-terraform

.PHONY: init validate up down reset plan calibrate e2e e2e-only e2e-factory agent-image

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

YNH_BASE ?= ghcr.io/eyelock/ynh:0.9.0
YNH_SRC ?=
AGENT_IMAGE ?= ynf-sandbox-agent:latest

agent-image:
	@set -e; arch=$$(docker version --format '{{.Server.Arch}}'); ctx=$$(mktemp -d); \
	trap 'rm -rf "$$ctx"; [ -z "$(YNH_SRC)" ] || git -C $(YNH_SRC) worktree remove --force "$$ctx/src" 2>/dev/null || true' EXIT; \
	cp images/agent/Dockerfile "$$ctx/"; mkdir -p "$$ctx/ynh"; \
	if [ -n "$(YNH_SRC)" ]; then \
	  git -C $(YNH_SRC) fetch -q origin develop; \
	  git -C $(YNH_SRC) worktree add -q --detach "$$ctx/src" origin/develop; \
	  (cd "$$ctx/src" && CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -o "$$ctx/ynh/ynh" ./cmd/ynh); \
	fi; \
	docker build -q --build-arg YNH_BASE=$(YNH_BASE) -t $(AGENT_IMAGE) "$$ctx"; \
	docker run --rm --network none --entrypoint ynh $(AGENT_IMAGE) version

FACTORY_BASE    ?= ynf-factory:dev
FACTORY_HARNESS ?= ynf-sandbox-harness:factory
FACTORY_LANES   ?= gofmt,deps

# Shape B: the sandbox's agent tools on the factory image, its harness in the factory flavour, and
# the acceptance test with ynf running inside that image, every run inline beside it.
e2e-factory: reset
	@set -e; ctx=$$(mktemp -d); trap 'rm -rf "$$ctx"' EXIT; \
	cp images/agent/Dockerfile "$$ctx/"; mkdir -p "$$ctx/ynh"; \
	docker build -q --build-arg YNH_BASE=$(FACTORY_BASE) -t ynf-sandbox-agent:factory "$$ctx" >/dev/null; \
	ynh image ynf-sandbox --from seed --base ynf-sandbox-agent:factory --entrypoint agent --tag $(FACTORY_HARNESS) >/dev/null; \
	echo "built $(FACTORY_HARNESS) on $(FACTORY_BASE)"
	cd e2e && go run . -root .. -lanes $(FACTORY_LANES) -image $(FACTORY_HARNESS)
