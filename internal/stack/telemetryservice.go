package stack

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/elenaochkina/dbtest/telemetry"
)

// telemetryService runs the Prometheus metrics server in this process.
type telemetryService struct {
	logs slog.Handler
	tel  *telemetry.Telemetry
	url  string
}

// newTelemetryService returns a telemetry service whose logger writes to logs.
func newTelemetryService(logs slog.Handler) *telemetryService {
	return &telemetryService{logs: logs}
}

// start starts telemetry on a free port unless it is already running,
// then checks that metrics are available.
// It returns the metrics URL.
func (s *telemetryService) start(ctx context.Context) (string, error) {
	if s.tel == nil {
		tel, err := telemetry.Init(telemetry.Config{Log: telemetry.LogConfig{Handler: s.logs}})
		if err != nil {
			return "", err
		}
		s.tel = tel
		s.url = fmt.Sprintf("http://localhost:%d/metrics", tel.Metrics.Port())
	}
	if err := s.check(ctx); err != nil {
		return "", err
	}
	return s.url, nil
}

// check fetches the metrics page.
func (s *telemetryService) check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("metrics server returned %s", response.Status)
	}
	return nil
}

func (s *telemetryService) stop() {
	if s.tel != nil {
		s.tel.Shutdown()
		s.tel = nil
	}
}
