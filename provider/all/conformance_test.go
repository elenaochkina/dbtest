//go:build conformance

package all

import (
	"strings"
	"testing"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/provider/dbservice"
	"github.com/elenaochkina/dbtest/provider/dbservice/dbservicetest"
)

// TestConformance provisions each service in DBTEST_CONFORMANCE (default
// docker) in its first cloud. Run it with go test -tags conformance; services
// other than docker create billed resources.
func TestConformance(t *testing.T) {
	services := cloud.EnvOr("DBTEST_CONFORMANCE", string(provider.Docker))
	for _, name := range strings.Split(services, ",") {
		t.Run(name, func(t *testing.T) {
			registration, ok := dbservice.Get(provider.DBServiceName(name))
			if !ok {
				t.Fatalf("%s is not a registered service", name)
			}
			c, err := cloud.NewCloudProvider(registration.Clouds()[0])
			if err != nil {
				t.Fatal(err)
			}
			if status := c.Check(t.Context()); status.State != provider.StateReady {
				t.Skipf("%s is %s: %s", c.Name(), status.State, status.Detail)
			}
			dbservicetest.Run(t, registration.Name(), c, provider.ProvisionRequest{VCPU: 2, MemoryMiB: 8192, PostgresVersion: cloud.EnvOr("DBTEST_CONFORMANCE_POSTGRES", "18")})
		})
	}
}
