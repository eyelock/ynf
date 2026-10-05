# The ynf sandbox: two GitHub repositories of your own, rebuilt from this folder. <owner>/ynf-sandbox,
# from seed/, holds the fixtures; <owner>/ynf-sandbox-factory, from factory-seed/, is the factory's
# configuration repository, which enrols the sandbox and gives it default lanes (ADR-006).
# Settings are in sandbox.env: copy sandbox.env.example and set SANDBOX_OWNER at least.
#   make up      create it (or bring it in line with this folder)
#   make reset   delete it and create it again from scratch
#   make down    delete it
#   make calibrate  prove every fixture can still tell a fixed state from an unfixed one (tests
#                   the fixtures, not the factory; needs ynh with .agents/harness support,
#                   YNH=/path/to/ynh to choose one)
#   make e2e        rebuild the sandbox, run ynf against it until everything settles, and check
#                   each fixture's expect block (tests the factory; LANES=gofmt,deps,reclaim,detect,spool,spool-image,spool-flood by default; ynh is hidden from ynf
#                   so the detect lane proves its fallback to the command: HIDE_YNH=false to unhide it).
#                   With ynr it also runs ynf as a factory job with a spool and the collector on, and
#                   checks manifests, run folders, the run quota and what ynr shipped: YNF_YNR_BIN=path
#                   names a ynr, YNR_SRC=path to a ynr checkout builds one (it only builds it), and
#                   ynr on PATH is used otherwise. With none, those checks are skipped, and it says why.
#   make e2e-only  the same, without the rebuild. Without a reset it leaves out shadow mode, whose
#                   stage merges a human fix for fmt-format into the sandbox main and so cannot
#                   run twice; SHADOW=true adds it. make e2e (and e2e-factory) reset first, so
#                   they run it with the gofmt lane; SHADOW=false skips it there
#   make e2e-memory-outage  e2e-only with ynm unreachable for the whole run, then brought back: the
#                   memory writes must queue in ynf's store, show in `ynf doctor`, and arrive
#                   after (ADR-008); it costs no more than e2e
#   make e2e-factory  the same with ynf inside the factory image, as a job runner would run it
#                   (ADR-009, shape B): the sandbox's harness built on ynf's factory image
#                   (make -C .. factory-image first; FACTORY_BASE names it)
#   make reset also empties the sandbox's ynm namespace (factory/github.com/<owner>/<name>), so every
#   e2e run starts from the same memory; e2e checks each item that ran left its memories
#   make agent-image  build ynf-sandbox-agent:latest, the base the ynh lanes' harness images use,
#                   on the ynh release image YNH_BASE; YNH_SRC=path to a ynh checkout builds its
#                   origin/develop into the image instead, to try ynh before it is released
# Deleting needs the delete_repo scope: gh auth refresh -s delete_repo
# State is local (terraform/terraform.tfstate) unless sandbox.env sets SANDBOX_STATE=s3 and the
# bucket; only then is AWS needed.

-include sandbox.env

SANDBOX_NAME       ?= ynf-sandbox
SANDBOX_FACTORY    ?= ynf-sandbox-factory
SANDBOX_VISIBILITY ?= private
SANDBOX_STATE_KEY  ?= sandbox/terraform.tfstate
SANDBOX_REPO        = $(SANDBOX_OWNER)/$(SANDBOX_NAME)
SANDBOX_FACTORY_REPO = $(SANDBOX_OWNER)/$(SANDBOX_FACTORY)

export TF_VAR_owner        = $(SANDBOX_OWNER)
export TF_VAR_name         = $(SANDBOX_NAME)
export TF_VAR_factory_name = $(SANDBOX_FACTORY)
export TF_VAR_visibility   = $(SANDBOX_VISIBILITY)
ifneq ($(AWS_PROFILE),)
export AWS_PROFILE
endif

TF := terraform -chdir=terraform
export GITHUB_TOKEN ?= $(shell gh auth token)

.PHONY: settings init validate up down reset plan calibrate e2e e2e-only e2e-memory-outage e2e-factory agent-image

settings:
	@[ -n "$(SANDBOX_OWNER)" ] || { echo "sandbox: no SANDBOX_OWNER. Copy sandbox.env.example to sandbox.env and set it (and the rest, if you want)."; exit 2; }

# Local state by default; with SANDBOX_STATE=s3, an S3 backend written from sandbox.env.
init: settings
	@set -e; if [ "$(SANDBOX_STATE)" = s3 ]; then \
	  [ -n "$(SANDBOX_STATE_BUCKET)" ] && [ -n "$(SANDBOX_STATE_REGION)" ] || { echo "sandbox: SANDBOX_STATE=s3 needs SANDBOX_STATE_BUCKET and SANDBOX_STATE_REGION"; exit 2; }; \
	  printf 'terraform {\n  backend "s3" {\n    bucket       = "%s"\n    key          = "%s"\n    region       = "%s"\n    use_lockfile = true\n    encrypt      = true\n  }\n}\n' \
	    "$(SANDBOX_STATE_BUCKET)" "$(SANDBOX_STATE_KEY)" "$(SANDBOX_STATE_REGION)" > terraform/backend_override.tf; \
	else rm -f terraform/backend_override.tf; fi
	$(TF) init -upgrade -reconfigure -input=false

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
	cd e2e && go run . -repo $(SANDBOX_REPO) -forget-memory

calibrate: settings
	cd calibrate && go run . -root .. -repo $(SANDBOX_REPO)

LANES ?= gofmt,deps,reclaim,detect,spool,spool-image,spool-flood
HIDE_YNH ?= true
SHADOW ?= false
# make e2e and e2e-factory reset first, so they run shadow mode unless told not to.
SHADOW_AFTER_RESET = $(if $(filter command line,$(origin SHADOW)),$(SHADOW),true)

e2e: reset
	$(MAKE) e2e-only SHADOW=$(SHADOW_AFTER_RESET)

e2e-only: settings
	cd e2e && go run . -root .. -repo $(SANDBOX_REPO) -factory $(SANDBOX_FACTORY_REPO) -lanes $(LANES) -hide-ynh=$(HIDE_YNH) -shadow=$(SHADOW)

YNH_BASE ?= ghcr.io/eyelock/ynh:0.10.0
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
# The outage lane's command always fails, so its item leaves a failure memory to queue, with no model.
OUTAGE_LANES ?= outage

e2e-memory-outage: reset settings
	cd e2e && go run . -root .. -repo $(SANDBOX_REPO) -factory $(SANDBOX_FACTORY_REPO) -lanes $(OUTAGE_LANES) -memory-outage -shadow=false

e2e-factory: reset
	@set -e; ctx=$$(mktemp -d); trap 'rm -rf "$$ctx"' EXIT; \
	cp images/agent/Dockerfile "$$ctx/"; mkdir -p "$$ctx/ynh"; \
	docker build -q --build-arg YNH_BASE=$(FACTORY_BASE) -t ynf-sandbox-agent:factory "$$ctx" >/dev/null; \
	ynh image ynf-sandbox --from seed --base ynf-sandbox-agent:factory --entrypoint agent --tag $(FACTORY_HARNESS) >/dev/null; \
	echo "built $(FACTORY_HARNESS) on $(FACTORY_BASE)"
	cd e2e && go run . -root .. -repo $(SANDBOX_REPO) -factory $(SANDBOX_FACTORY_REPO) -lanes $(FACTORY_LANES) -shadow=$(SHADOW_AFTER_RESET) -image $(FACTORY_HARNESS)
