package console

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/elenaochkina/dbtest/harness"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/provider/cloud"
	"github.com/elenaochkina/dbtest/provider/dbservice"
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

// openRecoveryForm returns the form on its first step, filled with the
// recovery defaults.
func openRecoveryForm() recoveryForm {
	return recoveryForm{cfg: defaultRecovery()}.show("cloud")
}

// steps returns the steps for the choices made so far.
func (form recoveryForm) steps() []string {
	steps := []string{"cloud", "database service"}
	if form.supportsHighAvailability() {
		steps = append(steps, "high availability")
	}
	steps = append(steps, "disruption", "repetitions", "size")
	if form.customSize {
		steps = append(steps, "vCPU", "memory MiB", "disk GiB", "scale")
	}
	return append(steps, "confirm")
}

// options returns the choices on the current step, or nil on a number step and on confirm.
func (form recoveryForm) options() []string {
	var labels []string
	switch form.step {
	case "cloud":
		for _, name := range cloud.Names() {
			labels = append(labels, string(name))
		}
	case "database service":
		for _, name := range dbservice.Names() {
			if cloud.Supports(form.cfg.Cloud, name) {
				labels = append(labels, string(name))
			}
		}
	case "high availability":
		labels = []string{"no", "yes"}
	case "disruption":
		for _, disruption := range dbservice.Disruptions(form.cfg.Provider, form.cfg.Request) {
			labels = append(labels, string(disruption))
		}
	case "size":
		labels = []string{"use defaults", "customize"}
	}
	return labels
}

// supportsHighAvailability reports whether the chosen service can run with a standby.
func (form recoveryForm) supportsHighAvailability() bool {
	registration, ok := dbservice.Get(form.cfg.Provider)
	return ok && registration.SupportsHighAvailability()
}

// show moves to step with an empty input. On a choice step the cursor is on
// the current choice if it is an option, otherwise on the first option.
func (form recoveryForm) show(step string) recoveryForm {
	form.step = step
	form.cursor = max(0, slices.Index(form.options(), form.current()))
	form.input = ""
	form.err = nil
	return form
}

// current returns the config's value for the current choice step.
func (form recoveryForm) current() string {
	switch form.step {
	case "cloud":
		return string(form.cfg.Cloud)
	case "database service":
		return string(form.cfg.Provider)
	case "high availability":
		if form.cfg.Request.HighAvailability {
			return "yes"
		}
		return "no"
	case "disruption":
		return string(form.cfg.Disruption)
	case "size":
		if form.customSize {
			return "customize"
		}
		return "use defaults"
	}
	return ""
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

// next saves the current choice, or the default if nothing was typed, and
// moves to the next step. It returns true after the last step. Invalid input
// keeps the step and shows an error.
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

// choose stores choice for the current choice step.
func (form recoveryForm) choose(choice string) recoveryForm {
	switch form.step {
	case "cloud":
		form.cfg.Cloud = provider.CloudName(choice)
		form.cfg.ProbeImage, form.cfg.BenchImage = "dbtest-probe", "dbtest-bench"
		if runsInDocker(form.cfg.Cloud) {
			form.cfg.ProbeImage, form.cfg.BenchImage = "dbtest/probe:dev", "dbtest/bench:dev"
		}
	case "database service":
		form.cfg.Provider = provider.DBServiceName(choice)
		form.cfg.Request.HighAvailability = false
	case "high availability":
		form.cfg.Request.HighAvailability = choice == "yes"
	case "disruption":
		form.cfg.Disruption = provider.Disruption(choice)
	case "size":
		form.customSize = choice == "customize"
	}
	return form
}

// runsInDocker reports whether the cloud starts the probe and bench containers
// with the Docker runner on this machine.
func runsInDocker(name provider.CloudName) bool {
	c, err := cloud.NewCloudProvider(name)
	return err == nil && c.Runners()[0] == harness.Docker
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
		return append(append(lines, form.summary()...), keyHelp("start", "enter", "back", "esc", "menu", "m", "exit", "q"))
	}
	return append(lines, keyHelp("next", "enter", "back", "esc", "menu", "m", "exit", "q"))
}

// summary returns one line per choice, and a cost warning outside the local cloud.
func (form recoveryForm) summary() []string {
	cfg := form.cfg
	disk := strconv.Itoa(cfg.Request.DiskGiB) + " GiB"
	if cfg.Request.DiskGiB == 0 {
		disk = "provider default"
	}
	rows := [][2]string{{"cloud", string(cfg.Cloud)}, {"database service", string(cfg.Provider)}}
	if form.supportsHighAvailability() {
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
	if cfg.Cloud != provider.Local {
		lines = append(lines, levelStyle(slog.LevelWarn).Render("creates "+string(cfg.Cloud)+" resources that cost money"))
	}
	return lines
}
