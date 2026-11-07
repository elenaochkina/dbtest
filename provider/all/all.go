// Package all registers every cloud and database service. Each binary that
// runs or lists them imports it once.
package all

import (
	_ "github.com/elenaochkina/dbtest/provider/cloud/aws"
	_ "github.com/elenaochkina/dbtest/provider/cloud/gcp"
	_ "github.com/elenaochkina/dbtest/provider/cloud/local"
	_ "github.com/elenaochkina/dbtest/provider/dbservice"
)
