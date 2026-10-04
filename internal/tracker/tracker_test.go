package tracker_test

import (
	"testing"

	"github.com/eyelock/ynf/internal/tracker"
)

func TestRefString(t *testing.T) {
	for r, want := range map[tracker.Ref]string{
		{Host: "github.com", Key: "eyelock/ynh#77"}:     "github.com/eyelock/ynh#77",
		{Host: "acme.atlassian.net", Key: "PLAT-881"}:   "acme.atlassian.net/PLAT-881",
		{Host: "github.acme.internal", Key: "acme/x#3"}: "github.acme.internal/acme/x#3",
	} {
		if r.String() != want {
			t.Errorf("%+v: %q", r, r.String())
		}
	}
}
