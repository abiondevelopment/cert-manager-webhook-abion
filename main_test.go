package main

import (
	"os"
	"testing"
	"time"

	acmetest "github.com/cert-manager/cert-manager/test/acme"
)

var zone = os.Getenv("TEST_ZONE_NAME")

func TestRunsSuite(t *testing.T) {
	solver := &abionDNSProviderSolver{}
	fixture := acmetest.NewFixture(solver,
		acmetest.SetResolvedZone(zone),
		acmetest.SetStrict(true),
		acmetest.SetAllowAmbientCredentials(false),
		acmetest.SetManifestPath("testdata/abion"),
		acmetest.SetPropagationLimit(time.Minute*10),
	)

	fixture.RunConformance(t)
}
