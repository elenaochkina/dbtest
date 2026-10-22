package pricing

import (
	"fmt"

	"github.com/elenaochkina/dbtest/provider"
	awsprovider "github.com/elenaochkina/dbtest/provider/aws"
)

// InstanceClass returns the instance class Provision creates on the named
// provider for request.
func InstanceClass(name provider.ProviderName, request provider.ProvisionRequest) string {
	switch name {
	case provider.RDS:
		return awsprovider.RDSInstanceClass(request)
	case provider.Aurora:
		return awsprovider.AuroraInstanceClass(request)
	}
	return ""
}

// HourlyCost returns the dollars per hour of a run: the database instances, RDS storage, and the probe and bench Fargate tasks.
// Aurora storage and I/O are not included.
func HourlyCost(prices Prices, name provider.ProviderName, request provider.ProvisionRequest) (float64, error) {
	class := InstanceClass(name, request)
	instance, instances, storage := "InstanceUsage:"+class, 1.0, ""
	switch name {
	case provider.RDS:
		storage = "RDS:GP2-Storage"
		if request.HighAvailability {
			instance, storage = "Multi-AZUsage:"+class, "RDS:Multi-AZ-GP2-Storage"
		}
	case provider.Aurora:
		if request.HighAvailability {
			instances = 2
		}
	}
	// The probe (0.25 vCPU, 0.5 GB) and bench (1 vCPU, 2 GB) task sizes come
	// from terraform/aws/taskdefs.tf.
	quantities := map[string]float64{
		instance:                        instances,
		"Fargate-ARM-vCPU-Hours:perCPU": 1.25,
		"Fargate-ARM-GB-Hours":          2.5,
	}
	if storage != "" {
		// Storage is priced per GB-month, which AWS counts as 730 hours.
		quantities[storage] = float64(awsprovider.RDSStorageGiB(request)) / 730
	}
	total := 0.0
	for usageType, quantity := range quantities {
		price, found := prices[usageType]
		if !found {
			return 0, fmt.Errorf("no price for %s", usageType)
		}
		total += price * quantity
	}
	return total, nil
}

// Cost returns the dollars for minutes at dollarsPerHour, with the 10-minute
// minimum RDS and Aurora bill.
func Cost(dollarsPerHour, minutes float64) float64 {
	return dollarsPerHour * max(minutes, 10) / 60
}
