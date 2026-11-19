package env

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRunDays(t *testing.T) {
	names := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	cases := map[string][]string{
		"tue":           {"tue"},
		"Tuesday":       {"tue"},
		"2":             {"tue"},
		"0":             {"sun"},
		"7":             {"sun"},
		"mon-fri":       {"mon", "tue", "wed", "thu", "fri"},
		"1-5":           {"mon", "tue", "wed", "thu", "fri"},
		"fri-sun":       {"sun", "fri", "sat"},
		"5-7":           {"sun", "fri", "sat"},
		"mon, thu":      {"mon", "thu"},
		"mon,wed-thu,6": {"mon", "wed", "thu", "sat"},
		"*":             names,
		"never":         nil,
		" NEVER ":       nil,
	}
	for pattern, want := range cases {
		days, err := parseRunDays(pattern)
		require.NoError(t, err, pattern)
		var got []string
		for i, on := range days {
			if on {
				got = append(got, names[i])
			}
		}
		require.Equal(t, want, got, pattern)
	}
	for _, pattern := range []string{"", "tuesdays", "8", "-1", "mon-", "0 4 * * 2", "tue;wed"} {
		_, err := parseRunDays(pattern)
		require.Error(t, err, pattern)
	}
}

func TestRunsOn(t *testing.T) {
	tuesday := time.Date(2026, 11, 24, 4, 31, 0, 0, time.UTC)
	wednesday := tuesday.AddDate(0, 0, 1)
	require.True(t, ModuleMeta{}.RunsOn(wednesday), "no run_days: every day")
	require.True(t, ModuleMeta{RunDays: "tue"}.RunsOn(tuesday))
	require.False(t, ModuleMeta{RunDays: "tue"}.RunsOn(wednesday))
	require.False(t, ModuleMeta{RunDays: "never"}.RunsOn(tuesday))
}

func TestRunDate(t *testing.T) {
	t.Setenv(RunDateEnv, "2026-11-24")
	date, err := RunDate()
	require.NoError(t, err)
	require.Equal(t, time.Tuesday, date.Weekday())
	t.Setenv(RunDateEnv, "24.11.2026")
	_, err = RunDate()
	require.Error(t, err)
	t.Setenv(RunDateEnv, "")
	date, err = RunDate()
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), date, time.Minute)
}

func TestModuleYamlRejectsBadRunDays(t *testing.T) {
	root := t.TempDir()
	writeEnv(t, root, "aws_exa", "steps:\n  - name: net\n    type: terraform\n    modules:\n      - source: aws/costly\n")
	dir := filepath.Join(root, "modules", "aws", "costly", "test")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ModuleFileName), []byte("run_days: someday\n"), 0o644))
	config, err := Load(root)
	if err == nil {
		_, err = config.ModuleBySource("aws/costly")
	}
	require.ErrorContains(t, err, "someday")
}
