`internal/format` is not `gofmt`-clean.

The spool-image lane's run keeps its image's own user, which is not the owner of its spool folder, and writes a record of its own into the folder before it formats the package.
