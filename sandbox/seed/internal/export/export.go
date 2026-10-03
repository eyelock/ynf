// Package export writes records as JSON lines.
package export

import (
	"encoding/json"
	"io"
)

// Record is one exported row.
type Record struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Write encodes each record on its own line.
func Write(w io.Writer, recs []Record) {
	enc := json.NewEncoder(w)
	for _, r := range recs {
		enc.Encode(r)
	}
}
