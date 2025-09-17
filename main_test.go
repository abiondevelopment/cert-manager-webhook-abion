// Copyright 2025 Abion AB
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"
	"time"

	acmetest "github.com/cert-manager/cert-manager/test/acme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var zone = os.Getenv("TEST_ZONE_NAME")

func TestMain(m *testing.M) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	os.Exit(m.Run())
}

func TestRunsSuite(t *testing.T) {
	solver := &abionDNSProviderSolver{}
	fixture := acmetest.NewFixture(solver,
		acmetest.SetResolvedZone(zone),
		acmetest.SetStrict(true),
		acmetest.SetAllowAmbientCredentials(false),
		acmetest.SetManifestPath("testdata/abion"),
		acmetest.SetPropagationLimit(time.Minute*20),
	)

	fixture.RunConformance(t)
}
