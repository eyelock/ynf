# A command lane's image with its own user, for `make e2e`: uid 10042 is not the owner of a run's
# spool folder, so the manifest must name it (ynr ADR-003, ynf ADR-011). Go for gofmt, no model.
FROM golang:1.26-alpine
RUN adduser -D -u 10042 probe
USER probe
