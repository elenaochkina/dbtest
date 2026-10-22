package console

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/workflows"
)

// recoveryForm asks for the recovery choices one step at a time. An empty
// step means the form is closed.
type recoveryForm struct {
	step       string
	cursor     int
	input      string
	err        error
	customSize bool
	cfg        workflows.RecoveryWorkflowConfig
}

var providerNames = map[string]provider.ProviderName{
	"docker":     provider.Docker,
	"aws-rds":    provider.RDS,
	"aws-aurora": provider.Aurora,
}

// openRecoveryForm returns the form on its first step, filled with the
// recovery defaults.
func openRecoveryForm() recoveryForm {
	return recoveryForm{cfg: defaultRecovery()}.show("provider")
}

// steps returns the steps for the choices made so far.
func (form recoveryForm) steps() []string {
	steps := []string{"provider"}
	if form.cfg.Provider != provider.Docker {
		steps = append(steps, "high availability")
	}
	steps = append(steps, "disruption", "repetitions", "size")
	if form.customSize {
		steps = append(steps, "vCPU", "memory MiB", "disk GiB", "scale")
	}
	return append(steps, "confirm")
}

// options returns the choices on the current step, or nil on a number step and
// on confirm.
func (form recoveryForm) options() []string {
	switch form.step {
	case "provider":
		return []string{"docker", "aws-rds", "aws-aurora"}
	case "high availability":
		return []string{"no", "yes"}
	case "disruption":
		return disruptions(form.cfg.Provider, form.cfg.Request.HighAvailability)
	case "size":
		return []string{"use defaults", "customize"}
	}
	return nil
}

// disruptions returns the disruptions the provider supports.
func disruptions(name provider.ProviderName, highAvailability bool) []string {
	var supported []string
	switch name {
	case provider.Docker, provider.Aurora:
		supported = []string{"restart", "crash"}
	case provider.RDS:
		supported = []string{"restart"}
	}
	if highAvailability {
		supported = append(supported, "failover")
	}
	return supported
}

// show moves to step with an empty input. On a choice step the cursor is on
// crash if it is an option, otherwise on the first option.
func (form recoveryForm) show(step string) recoveryForm {
	form.step = step
	form.cursor = max(0, slices.Index(form.options(), "crash"))
	form.input = ""
	form.err = nil
	return form
}

// move moves the cursor by offset within the options.
func (form recoveryForm) move(offset int) recoveryForm {
	if options := form.options(); options != nil {
		form.cursor = min(max(0, form.cursor+offset), len(options)-1)
	}
	return form
}

// typeText adds a digit, or a dot for vCPU, to the input on a number step.
func (form recoveryForm) typeText(text string) recoveryForm {
	allowed := "0123456789"
	if form.step == "vCPU" {
		allowed += "."
	}
	if form.isNumberStep() && len(text) == 1 && strings.Contains(allowed, text) {
		form.input += text
	}
	return form
}

// erase removes the last character of the input.
func (form recoveryForm) erase() recoveryForm {
	if form.input != "" {
		form.input = form.input[:len(form.input)-1]
	}
	return form
}

// next stores the option under the cursor or the input, or the default when the
// input is empty, then shows the following step. It reports true after the last
// step. An invalid input stays on the step with an error.
func (form recoveryForm) next() (recoveryForm, bool) {
	if options := form.options(); options != nil {
		form = form.choose(options[form.cursor])
	} else if form.isNumberStep() {
		text := form.input
		if text == "" {
			text = form.defaultValue()
		}
		var err error
		if form, err = form.set(text); err != nil {
			form.err = err
			return form, false
		}
	}
	steps := form.steps()
	index := slices.Index(steps, form.step)
	if index == len(steps)-1 {
		return form, true
	}
	return form.show(steps[index+1]), false
}

// back shows the previous step, or closes the form on the first step.
func (form recoveryForm) back() recoveryForm {
	steps := form.steps()
	if index := slices.Index(steps, form.step); index > 0 {
		return form.show(steps[index-1])
	}
	return recoveryForm{}
}

// choose stores choice for the current choice step. On AWS the probe and bench
// images are the Fargate task definition names.
func (form recoveryForm) choose(choice string) recoveryForm {
	switch form.step {
	case "provider":
		form.cfg.Provider = providerNames[choice]
		form.cfg.Request.HighAvailability = false
		form.cfg.ProbeImage, form.cfg.BenchImage = "dbtest/probe:dev", "dbtest/bench:dev"
		if form.cfg.Provider != provider.Docker {
			form.cfg.ProbeImage, form.cfg.BenchImage = "dbtest-probe", "dbtest-bench"
		}
	case "high availability":
		form.cfg.Request.HighAvailability = choice == "yes"
	case "disruption":
		form.cfg.Disruption = provider.Disruption(choice)
	case "size":
		form.customSize = choice == "customize"
	}
	return form
}

// isNumberStep reports whether the current step takes a number.
func (form recoveryForm) isNumberStep() bool {
	return form.defaultValue() != ""
}

// defaultValue returns the default of the number step as text.
func (form recoveryForm) defaultValue() string {
	cfg := defaultRecovery()
	switch form.step {
	case "repetitions":
		return strconv.Itoa(cfg.Repetitions)
	case "vCPU":
		return strconv.FormatFloat(cfg.Request.VCPU, 'f', -1, 64)
	case "memory MiB":
		return strconv.Itoa(cfg.Request.MemoryMiB)
	case "disk GiB":
		return strconv.Itoa(cfg.Request.DiskGiB)
	case "scale":
		return strconv.Itoa(cfg.Scale)
	}
	return ""
}

// set parses text and stores it for the current number step.
func (form recoveryForm) set(text string) (recoveryForm, error) {
	if form.step == "vCPU" {
		vcpu, err := strconv.ParseFloat(text, 64)
		if err != nil || vcpu <= 0 {
			return form, errors.New("enter a number above 0")
		}
		form.cfg.Request.VCPU = vcpu
		return form, nil
	}
	minimum := 1
	if form.step == "disk GiB" {
		minimum = 0
	}
	number, err := strconv.Atoi(text)
	if err != nil || number < minimum {
		return form, fmt.Errorf("enter a whole number, %d or more", minimum)
	}
	switch form.step {
	case "repetitions":
		form.cfg.Repetitions = number
	case "memory MiB":
		form.cfg.Request.MemoryMiB = number
	case "disk GiB":
		form.cfg.Request.DiskGiB = number
	case "scale":
		form.cfg.Scale = number
	}
	return form, nil
}

// lines draws the step's options with the cursor, the input with its error, or
// the summary, and the keys.
func (form recoveryForm) lines() []string {
	var lines []string
	for index, option := range form.options() {
		if index == form.cursor {
			lines = append(lines, cursorStyle.Render("> "+option))
		} else {
			lines = append(lines, "  "+option)
		}
	}
	if form.isNumberStep() {
		lines = append(lines, cursorStyle.Render("> "+form.input+"_")+helpStyle.Render("  default "+form.defaultValue()))
		if form.err != nil {
			lines = append(lines, levelStyle(slog.LevelError).Render(form.err.Error()))
		}
		if form.step == "disk GiB" {
			lines = append(lines, helpStyle.Render("0 uses the provider's default"))
		}
	}
	if form.step == "confirm" {
		return append(append(lines, form.summary()...), helpStyle.Render("enter start · esc back"))
	}
	return append(lines, helpStyle.Render("enter next · esc back"))
}

// summary returns one line per choice, and a warning for AWS providers.
func (form recoveryForm) summary() []string {
	cfg := form.cfg
	disk := strconv.Itoa(cfg.Request.DiskGiB) + " GiB"
	if cfg.Request.DiskGiB == 0 {
		disk = "provider default"
	}
	rows := [][2]string{{"provider", providerLabel(cfg.Provider)}}
	if cfg.Provider != provider.Docker {
		highAvailability := "no"
		if cfg.Request.HighAvailability {
			highAvailability = "yes"
		}
		rows = append(rows, [2]string{"high availability", highAvailability})
	}
	rows = append(rows,
		[2]string{"disruption", string(cfg.Disruption)},
		[2]string{"repetitions", strconv.Itoa(cfg.Repetitions)},
		[2]string{"vCPU", strconv.FormatFloat(cfg.Request.VCPU, 'f', -1, 64)},
		[2]string{"memory", strconv.Itoa(cfg.Request.MemoryMiB) + " MiB"},
		[2]string{"disk", disk},
		[2]string{"scale", strconv.Itoa(cfg.Scale)},
	)
	var lines []string
	for _, row := range rows {
		lines = append(lines, padRight(row[0], 19)+row[1])
	}
	if cfg.Provider != provider.Docker {
		lines = append(lines, levelStyle(slog.LevelWarn).Render("creates AWS resources that cost money"))
	}
	return lines
}

// providerLabel returns the form's label for name.
func providerLabel(name provider.ProviderName) string {
	for label, providerName := range providerNames {
		if providerName == name {
			return label
		}
	}
	return string(name)
}
