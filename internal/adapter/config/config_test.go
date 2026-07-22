package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoad_Defaults(t *testing.T) {
	path := writeTempConfig(t, `
signalPort: 9001
gateListenAddr: "0.0.0.0:9000"
hardcore:
  workDir: "./hardcore"
  startCommand: ["java", "-jar", "server.jar", "nogui"]
archive:
  dir: "./archive"
`)

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if c.SignalPort != 9001 {
		t.Errorf("SignalPort = %d, want 9001", c.SignalPort)
	}
	if c.Hardcore.RecordsDir != DefaultRecordsDir {
		t.Errorf("RecordsDir = %q, want default %q", c.Hardcore.RecordsDir, DefaultRecordsDir)
	}
	if c.Timeouts.EvacuateCompleteSeconds != DefaultEvacuateCompleteSeconds {
		t.Errorf("EvacuateCompleteSeconds = %d, want default %d", c.Timeouts.EvacuateCompleteSeconds, DefaultEvacuateCompleteSeconds)
	}
	if c.Timeouts.HardcoreReadySeconds != DefaultHardcoreReadySeconds {
		t.Errorf("HardcoreReadySeconds = %d, want default %d", c.Timeouts.HardcoreReadySeconds, DefaultHardcoreReadySeconds)
	}
	if c.Timeouts.ProcessStopSeconds != DefaultProcessStopSeconds {
		t.Errorf("ProcessStopSeconds = %d, want default %d", c.Timeouts.ProcessStopSeconds, DefaultProcessStopSeconds)
	}
	if c.State.Path != DefaultStatePath {
		t.Errorf("State.Path = %q, want default %q", c.State.Path, DefaultStatePath)
	}
	if c.Hardcore.PidFile != DefaultPidFile {
		t.Errorf("Hardcore.PidFile = %q, want default %q", c.Hardcore.PidFile, DefaultPidFile)
	}
}

func TestLoad_ExplicitValuesOverrideDefaults(t *testing.T) {
	path := writeTempConfig(t, `
signalPort: 9001
gateListenAddr: "0.0.0.0:9000"
state:
  path: "./custom-state.json"
hardcore:
  workDir: "./hardcore"
  startCommand: ["java", "-jar", "server.jar"]
  recordsDir: "custom-records"
  pidFile: "./custom-hardcore.pid"
archive:
  dir: "./archive"
timeouts:
  evacuateCompleteSeconds: 5
  hardcoreReadySeconds: 10
  processStopSeconds: 15
`)

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Hardcore.RecordsDir != "custom-records" {
		t.Errorf("RecordsDir = %q, want custom-records", c.Hardcore.RecordsDir)
	}
	if c.Hardcore.PidFile != "./custom-hardcore.pid" {
		t.Errorf("PidFile = %q, want ./custom-hardcore.pid", c.Hardcore.PidFile)
	}
	if c.State.Path != "./custom-state.json" {
		t.Errorf("State.Path = %q, want ./custom-state.json", c.State.Path)
	}
	if c.Timeouts.EvacuateCompleteSeconds != 5 {
		t.Errorf("EvacuateCompleteSeconds = %d, want 5", c.Timeouts.EvacuateCompleteSeconds)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	cases := map[string]string{
		"missing signalPort": `
gateListenAddr: "0.0.0.0:9000"
hardcore:
  workDir: "./hardcore"
  startCommand: ["java"]
archive:
  dir: "./archive"
`,
		"missing gateListenAddr": `
signalPort: 9001
hardcore:
  workDir: "./hardcore"
  startCommand: ["java"]
archive:
  dir: "./archive"
`,
		"missing hardcore.workDir": `
signalPort: 9001
gateListenAddr: "0.0.0.0:9000"
hardcore:
  startCommand: ["java"]
archive:
  dir: "./archive"
`,
		"missing hardcore.startCommand": `
signalPort: 9001
gateListenAddr: "0.0.0.0:9000"
hardcore:
  workDir: "./hardcore"
archive:
  dir: "./archive"
`,
		"missing archive.dir": `
signalPort: 9001
gateListenAddr: "0.0.0.0:9000"
hardcore:
  workDir: "./hardcore"
  startCommand: ["java"]
`,
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeTempConfig(t, content)
			if _, err := Load(path); err == nil {
				t.Fatalf("expected validation error for %s", name)
			}
		})
	}
}

func TestHardcorePaths(t *testing.T) {
	h := Hardcore{WorkDir: "hardcore", RecordsDir: "records"}
	if got, want := h.WorldDir(), filepath.Join("hardcore", "world"); got != want {
		t.Errorf("WorldDir() = %q, want %q", got, want)
	}
	if got, want := h.RecordsPath(), filepath.Join("hardcore", "records"); got != want {
		t.Errorf("RecordsPath() = %q, want %q", got, want)
	}
	if got, want := h.ServerPropertiesPath(), filepath.Join("hardcore", "server.properties"); got != want {
		t.Errorf("ServerPropertiesPath() = %q, want %q", got, want)
	}
}

func TestArchiveNamedDir(t *testing.T) {
	a := Archive{Dir: "archive"}
	if got, want := a.NamedDir("save1"), filepath.Join("archive", "save1"); got != want {
		t.Errorf("NamedDir() = %q, want %q", got, want)
	}
}
