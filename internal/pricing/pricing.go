// Package pricing fetches AWS on-demand prices from the Price List API.
package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/pricing/types"

	"github.com/elenaochkina/dbtest/provider"
)

// Prices holds on-demand prices in dollars per unit.
// Instances and Fargate are priced per hour, storage per GB-month.
type Prices map[string]float64

type pricingConfig struct {
	Region string
}

func loadConfig() pricingConfig {
	return pricingConfig{Region: os.Getenv("AWS_REGION")}
}

type query struct {
	service    string
	attributes []string
}

// Client fetches prices for AWS_REGION from the Price List API in us-east-1.
type Client struct {
	pricing *awspricing.Client
	region  string
}

func New(ctx context.Context) (*Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &Client{pricing: awspricing.NewFromConfig(awsCfg), region: loadConfig().Region}, nil
}

// Returns Prices map.
func (c *Client) Fetch(ctx context.Context, name provider.ProviderName, instanceClass string) (Prices, error) {
	fargate := query{"AmazonECS", []string{"cpuArchitecture", "ARM"}}
	var queries []query
	switch name {
	case provider.RDS:
		queries = []query{
			{"AmazonRDS", []string{"productFamily", "Database Instance", "databaseEngine", "PostgreSQL", "instanceType", instanceClass}},
			{"AmazonRDS", []string{"productFamily", "Database Storage", "databaseEngine", "PostgreSQL", "volumeType", "General Purpose"}},
			fargate,
		}
	case provider.Aurora:
		queries = []query{
			{"AmazonRDS", []string{"productFamily", "Database Instance", "databaseEngine", "Aurora PostgreSQL", "instanceType", instanceClass}},
			fargate,
		}
	default:
		return nil, fmt.Errorf("no prices for provider %s", name)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	prices := Prices{}
	for _, search := range queries {
		input := &awspricing.GetProductsInput{
			ServiceCode: aws.String(search.service),
			Filters:     termFilters(append([]string{"regionCode", c.region}, search.attributes...)),
		}
		pages := awspricing.NewGetProductsPaginator(c.pricing, input)
		for pages.HasMorePages() {
			page, err := pages.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("get %s prices: %w", search.service, err)
			}
			for _, document := range page.PriceList {
				var item product
				if err := json.Unmarshal([]byte(document), &item); err != nil {
					return nil, fmt.Errorf("parse %s price: %w", search.service, err)
				}
				if price, found := item.onDemandUSD(); found {
					prices[withoutRegion(item.Product.Attributes.UsageType)] = price
				}
			}
		}
	}
	return prices, nil
}

// termFilters turns name and value pairs into exact-match filters.
func termFilters(namesAndValues []string) []types.Filter {
	var filters []types.Filter
	for index := 0; index+1 < len(namesAndValues); index += 2 {
		filters = append(filters, types.Filter{
			Type:  types.FilterTypeTermMatch,
			Field: aws.String(namesAndValues[index]),
			Value: aws.String(namesAndValues[index+1]),
		})
	}
	return filters
}

// product holds the usage type and on-demand price from a Price List document;
// json.Unmarshal skips the other fields.
type product struct {
	Product struct {
		Attributes struct {
			UsageType string `json:"usagetype"`
		} `json:"attributes"`
	} `json:"product"`
	Terms struct {
		OnDemand map[string]struct {
			PriceDimensions map[string]struct {
				PricePerUnit struct {
					USD string
				} `json:"pricePerUnit"`
			} `json:"priceDimensions"`
		} `json:"OnDemand"`
	} `json:"terms"`
}

// onDemandUSD returns the product's on-demand price in dollars.
func (item product) onDemandUSD() (float64, bool) {
	for _, term := range item.Terms.OnDemand {
		for _, dimension := range term.PriceDimensions {
			if price, err := strconv.ParseFloat(dimension.PricePerUnit.USD, 64); err == nil {
				return price, true
			}
		}
	}
	return 0, false
}

// withoutRegion turns "USW2-InstanceUsage:db.t3.small" into "InstanceUsage:db.t3.small".
func withoutRegion(usageType string) string {
	if code, rest, found := strings.Cut(usageType, "-"); found && strings.ContainsAny(code, "0123456789") {
		return rest
	}
	return usageType
}
