package console

import "github.com/elenaochkina/dbtest/internal/service"

// Grafana dashboards: a name and the path after Grafana's URL.
var dashboards = [][2]string{
	{"Recovery dashboard", "/d/dbtest-recovery"},
}

// dashboardLines returns each dashboard's name and its link after Grafana is up.
func (m model) dashboardLines() []string {
	var grafanaURL string
	for _, status := range m.services {
		if status.name == "grafana" && status.state == service.StateUp {
			grafanaURL = status.detail
		}
	}
	var lines []string
	for _, dashboard := range dashboards {
		lines = append(lines, titleStyle.Render(dashboard[0]))
		if grafanaURL == "" {
			lines = append(lines, helpStyle.Render("waiting for grafana"))
		} else {
			lines = append(lines, grafanaURL+dashboard[1])
		}
	}
	return lines
}
