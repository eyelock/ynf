The outage lane's command always fails, so this item always escalates and always leaves a failure
memory. `make e2e-memory-outage` uses it to prove that memory writes queue while ynm is unreachable
and arrive when it is back; it needs no model.
