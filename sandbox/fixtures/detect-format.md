`internal/format` is not `gofmt`-clean.

The sandbox's runner detection test: the lane names no runner, so ynf runs ynh when it finds one and
the lane's command when it does not. The acceptance test hides ynh, so this proves the fallback.
