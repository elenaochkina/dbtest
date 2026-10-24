package console

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/elenaochkina/dbtest/internal/logbus"
	"github.com/elenaochkina/dbtest/internal/providercfg"
	"github.com/elenaochkina/dbtest/internal/stack"
	"github.com/elenaochkina/dbtest/provider"
	"github.com/elenaochkina/dbtest/temporal/workflows"
)

// newTestModel returns a model with all components starting, no event or log
// feed, a retry that does nothing and an AWS check that reports ready.
func newTestModel() model {
	return model{
		components: []component{
			{name: "state db", state: stack.StateStarting},
			{name: "temporal", state: stack.StateStarting},
			{name: "telemetry", state: stack.StateStarting},
			{name: "worker", state: stack.StateStarting},
		},
		retry: func() {},
		checkAWS: func(context.Context) providercfg.Status {
			return providercfg.Status{State: providercfg.StateReady, Detail: "role dbtest"}
		},
	}
}

// send passes each message to Update and returns the resulting model.
func send(current model, messages ...tea.Msg) model {
	for _, message := range messages {
		next, _ := current.Update(message)
		current = next.(model)
	}
	return current
}

func keyPress(key string) tea.KeyPressMsg {
	switch key {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
}

func TestCursorStopsAtTop(t *testing.T) {
	current := send(newTestModel(), keyPress("right"), keyPress("left"), keyPress("left"))
	if current.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", current.cursor)
	}
}

func TestCursorStopsAtBottom(t *testing.T) {
	current := newTestModel()
	for range len(actions) + 3 {
		current = send(current, keyPress("right"))
	}
	if want := len(actions) - 1; current.cursor != want {
		t.Fatalf("cursor = %d, want %d", current.cursor, want)
	}
}

func TestDigitSelectsAction(t *testing.T) {
	current := send(newTestModel(), keyPress("3"))
	if current.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", current.cursor)
	}
}

func TestQuitKeys(t *testing.T) {
	for _, keys := range [][]string{{"q"}, {"4", "enter"}} {
		current := newTestModel()
		var command tea.Cmd
		for _, key := range keys {
			var next tea.Model
			next, command = current.Update(keyPress(key))
			current = next.(model)
		}
		if command == nil {
			t.Fatalf("%v: no command, want quit", keys)
		}
		if _, isQuit := command().(tea.QuitMsg); !isQuit {
			t.Fatalf("%v: command is not quit", keys)
		}
	}
}

func TestEnterOnOtherActionDoesNotQuit(t *testing.T) {
	_, command := send(newTestModel(), keyPress("1")).Update(keyPress("enter"))
	if command != nil {
		t.Fatal("enter on start pgbench returned a command")
	}
}

// startRecoveryWithKeys opens the recovery form, presses keys, and returns
// the configs started and the resulting model.
func startRecoveryWithKeys(t *testing.T, keys ...string) ([]workflows.RecoveryWorkflowConfig, model) {
	t.Helper()
	var started []workflows.RecoveryWorkflowConfig
	current := send(newTestModel(), keyPress("2"), keyPress("enter"))
	current.startRecovery = func(cfg workflows.RecoveryWorkflowConfig) (string, error) {
		started = append(started, cfg)
		return "recovery-test", nil
	}
	for _, key := range keys {
		next, command := current.Update(keyPress(key))
		current = next.(model)
		if command != nil {
			command()
		}
	}
	return started, current
}

func TestRecoveryFormDockerSkipsHighAvailability(t *testing.T) {
	_, current := startRecoveryWithKeys(t, "enter")
	if current.form.step != "disruption" {
		t.Fatalf("step = %q, want disruption", current.form.step)
	}
	started, _ := startRecoveryWithKeys(t, "enter", "enter", "enter", "enter", "enter")
	want := defaultRecovery()
	want.Disruption = provider.Crash
	if len(started) != 1 || started[0] != want {
		t.Fatalf("started %+v, want one docker crash recovery", started)
	}
}

func TestRecoveryFormDisruptionsAndDefaults(t *testing.T) {
	for _, test := range []struct {
		keys    []string
		options []string
		want    string
	}{
		{[]string{"down", "enter", "enter"}, []string{"restart"}, "restart"},
		{[]string{"down", "enter", "down", "enter"}, []string{"restart", "failover"}, "restart"},
		{[]string{"down", "down", "enter", "enter"}, []string{"restart", "crash"}, "crash"},
		{[]string{"down", "down", "enter", "down", "enter"}, []string{"restart", "crash", "failover"}, "crash"},
	} {
		_, current := startRecoveryWithKeys(t, test.keys...)
		options := current.form.options()
		if !slices.Equal(options, test.options) || options[current.form.cursor] != test.want {
			t.Errorf("%v: options %v at %d, want %v at %s", test.keys, options, current.form.cursor, test.options, test.want)
		}
	}
}

func TestRecoveryFormStartsAuroraFailover(t *testing.T) {
	started, current := startRecoveryWithKeys(t, "down", "down", "enter", "down", "enter", "down", "enter", "enter", "enter", "enter")
	if len(started) != 1 {
		t.Fatalf("started %d recoveries, want 1", len(started))
	}
	cfg := started[0]
	if cfg.Provider != provider.Aurora || !cfg.Request.HighAvailability || cfg.Disruption != provider.Failover {
		t.Fatalf("started %+v, want aurora HA failover", cfg)
	}
	if cfg.ProbeImage != "dbtest-probe" || cfg.BenchImage != "dbtest-bench" {
		t.Fatalf("images = %s, %s, want the Fargate task definitions", cfg.ProbeImage, cfg.BenchImage)
	}
	if current.form.step != "" {
		t.Fatalf("form still open on %q", current.form.step)
	}
}

func TestRecoveryFormEscapeGoesBackThenCloses(t *testing.T) {
	_, current := startRecoveryWithKeys(t, "down", "enter", "enter", "esc")
	if current.form.step != "high availability" {
		t.Fatalf("step = %q, want high availability", current.form.step)
	}
	current = send(current, keyPress("esc"), keyPress("esc"))
	if current.form.step != "" {
		t.Fatalf("form still open on %q", current.form.step)
	}
}

func TestRecoveryFormMenuKeyCloses(t *testing.T) {
	_, current := startRecoveryWithKeys(t, "down", "enter", "enter", "m")
	if current.form.step != "" {
		t.Fatalf("form still open on %q", current.form.step)
	}
}

func TestRecoveryFormStartsTwice(t *testing.T) {
	started, _ := startRecoveryWithKeys(t, "enter", "enter", "enter", "enter", "enter", "enter", "enter", "enter", "enter", "enter", "enter")
	if len(started) != 2 {
		t.Fatalf("started %d recoveries, want 2", len(started))
	}
}

func TestRecoveryFormStartsCustomSize(t *testing.T) {
	started, _ := startRecoveryWithKeys(t,
		"enter", "enter", // docker, crash
		"5", "enter", // repetitions
		"down", "enter", // customize
		"0", ".", "5", "enter", // vCPU
		"1", "0", "2", "4", "enter", // memory MiB
		"2", "0", "enter", // disk GiB
		"enter", // scale
		"enter", // confirm
	)
	want := defaultRecovery()
	want.Disruption = provider.Crash
	want.Repetitions = 5
	want.Request.VCPU = 0.5
	want.Request.MemoryMiB = 1024
	want.Request.DiskGiB = 20
	if len(started) != 1 || started[0] != want {
		t.Fatalf("started %+v, want %+v", started, want)
	}
}

func TestRecoveryFormRejectsInvalidNumber(t *testing.T) {
	started, current := startRecoveryWithKeys(t, "enter", "enter", "0", "enter")
	if current.form.step != "repetitions" || current.form.err == nil {
		t.Fatalf("step = %q, err = %v, want repetitions with an error", current.form.step, current.form.err)
	}
	current = send(current, keyPress("backspace"), keyPress("x"), keyPress("2"), keyPress("enter"))
	if current.form.step != "size" || current.form.cfg.Repetitions != 2 {
		t.Fatalf("step = %q, repetitions = %d, want size and 2", current.form.step, current.form.cfg.Repetitions)
	}
	if len(started) != 0 {
		t.Fatal("started a recovery")
	}
}

func TestRecoveryFormQuitKeyQuits(t *testing.T) {
	_, command := send(newTestModel(), keyPress("2"), keyPress("enter")).Update(keyPress("q"))
	if command == nil {
		t.Fatal("q in the form returned no command")
	}
	if _, isQuit := command().(tea.QuitMsg); !isQuit {
		t.Fatal("q in the form did not quit")
	}
}

func TestRecoveryFormConfirmShowsChoices(t *testing.T) {
	for _, test := range []struct {
		keys        []string
		want        []string
		wantWarning bool
	}{
		{[]string{"enter", "enter", "enter", "enter"}, []string{"docker", "crash", "memory             2048 MiB", "disk               provider default"}, false},
		{[]string{"down", "down", "enter", "down", "enter", "down", "enter", "enter", "enter"}, []string{"aws-aurora", "high availability  yes", "failover"}, true},
	} {
		started, current := startRecoveryWithKeys(t, test.keys...)
		if current.form.step != "confirm" || len(started) != 0 {
			t.Fatalf("%v: step = %q, started %d, want confirm and none started", test.keys, current.form.step, len(started))
		}
		summary := strings.Join(current.form.lines(), "\n")
		for _, want := range test.want {
			if !strings.Contains(summary, want) {
				t.Errorf("%v: summary has no %q:\n%s", test.keys, want, summary)
			}
		}
		if hasWarning := strings.Contains(summary, "AWS resources"); hasWarning != test.wantWarning {
			t.Errorf("%v: AWS warning shown = %v, want %v", test.keys, hasWarning, test.wantWarning)
		}
	}
}

func TestRecoveryFormEscapeFromConfirmGoesBack(t *testing.T) {
	_, current := startRecoveryWithKeys(t, "enter", "enter", "enter", "enter", "esc")
	if current.form.step != "size" {
		t.Fatalf("step = %q, want size", current.form.step)
	}
}

func TestComponentStateIsStored(t *testing.T) {
	current := send(newTestModel(), stack.Event{Name: "temporal", State: stack.StateUp, Detail: "localhost:7233"})
	temporal := current.components[1]
	if temporal.state != stack.StateUp || temporal.detail != "localhost:7233" {
		t.Fatalf("temporal = %+v, want up at localhost:7233", temporal)
	}
	if current.components[0].state != stack.StateStarting {
		t.Fatal("state db changed too")
	}
}

func TestStatusShowsOneLinePerDetailLine(t *testing.T) {
	current := send(newTestModel(), stack.Event{Name: "temporal", State: stack.StateUp, Detail: "API\tlocalhost:7233\nUI\thttp://localhost:8233"})
	lines := current.statusLines()
	if len(lines) != 6 {
		t.Fatalf("got %d status lines, want 6", len(lines))
	}
	for index, want := range [][2]string{{"temporal API", "localhost:7233"}, {"temporal UI", "http://localhost:8233"}} {
		if line := lines[1+index]; !strings.HasPrefix(line, want[0]+" ") || !strings.HasSuffix(line, " "+want[1]) {
			t.Fatalf("line %d = %q, want %s ... %s", 1+index, line, want[0], want[1])
		}
	}
}

func TestRetryKeyCallsRetry(t *testing.T) {
	retries := 0
	current := newTestModel()
	current.retry = func() { retries++ }
	send(current, keyPress("r"))
	if retries != 1 {
		t.Fatalf("retry called %d times, want 1", retries)
	}
}

func TestLogKeepsNewestLines(t *testing.T) {
	batch := make(logBatchMsg, logCapacity+5)
	for index := range batch {
		batch[index] = logbus.Line{Time: time.Now(), Message: fmt.Sprint(index)}
	}
	current := send(newTestModel(), batch)
	if len(current.log) != logCapacity {
		t.Fatalf("log has %d lines, want %d", len(current.log), logCapacity)
	}
	if current.log[0].Message != "5" {
		t.Fatalf("oldest line = %q, want 5", current.log[0].Message)
	}
}

func TestRenderFillsWindow(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{Width: 60, Height: 15}, {Width: 80, Height: 24}, {Width: 200, Height: 60}} {
		lines := strings.Split(send(newTestModel(), size).render(), "\n")
		if len(lines) != size.Height {
			t.Errorf("%dx%d: %d lines", size.Width, size.Height, len(lines))
		}
		for number, line := range lines {
			if width := lipgloss.Width(line); width != size.Width {
				t.Errorf("%dx%d: line %d is %d wide", size.Width, size.Height, number, width)
			}
		}
	}
}

func TestRenderTooSmall(t *testing.T) {
	current := send(newTestModel(), tea.WindowSizeMsg{Width: 30, Height: 8})
	if got := current.render(); got != "terminal too small" {
		t.Fatalf("render = %q, want terminal too small", got)
	}
}

func TestAWSRowShowsCheckingThenResult(t *testing.T) {
	current := newTestModel()
	if line := current.statusLines()[4]; !strings.HasPrefix(line, "aws ") || !strings.Contains(line, "checking") {
		t.Fatalf("aws line = %q, want checking", line)
	}
	current = send(current, awsStatusMsg{State: providercfg.StateNeedsLogin, Detail: "token expired"})
	if line := current.statusLines()[4]; !strings.Contains(line, "needs login") || !strings.HasSuffix(line, " token expired") {
		t.Fatalf("aws line = %q, want needs login and the detail", line)
	}
}

func TestRetryKeyChecksAWSAgain(t *testing.T) {
	checks := 0
	current := send(newTestModel(), awsStatusMsg{State: providercfg.StateReady, Detail: "role dbtest"})
	current.checkAWS = func(context.Context) providercfg.Status {
		checks++
		return providercfg.Status{State: providercfg.StateMisconfigured, Detail: "missing AWS_REGION"}
	}
	next, command := current.Update(keyPress("r"))
	current = next.(model)
	if current.aws.State != "" || command == nil {
		t.Fatalf("aws = %+v, command = %v, want checking and a command", current.aws, command)
	}
	current = send(current, command())
	if checks != 1 || current.aws.State != providercfg.StateMisconfigured {
		t.Fatalf("checks = %d, aws = %+v, want 1 check and misconfigured", checks, current.aws)
	}
}
