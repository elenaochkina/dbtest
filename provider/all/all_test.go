package all

import (
	"testing"

	"github.com/elenaochkina/dbtest/provider/dbservice"
	"github.com/elenaochkina/dbtest/provider/dbservice/dbservicetest"
)

func TestEveryRegistrationKeepsTheContract(t *testing.T) {
	for _, name := range dbservice.Names() {
		t.Run(string(name), func(t *testing.T) {
			registration, _ := dbservice.Get(name)
			dbservicetest.CheckRegistration(t, registration)
		})
	}
}
