package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout runs f and returns what it printed to stdout.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()
	f()
	w.Close()
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(data)
}

func TestFormatAnnouncesStartAndFinish(t *testing.T) {
	config := fixture(t)
	pkg := `"Package":"example.com/modules/modules/aws/vpc/test"`
	events := []string{
		// A parent that only runs a parallel subtest: announced once the
		// subtest body starts, not when go test registers or pauses it.
		`{"Action":"run",` + pkg + `,"Test":"TestVpc"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestVpc","Output":"=== RUN   TestVpc\n"}`,
		`{"Action":"run",` + pkg + `,"Test":"TestVpc/aws_exa"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestVpc/aws_exa","Output":"=== RUN   TestVpc/aws_exa\n"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestVpc/aws_exa","Output":"=== PAUSE TestVpc/aws_exa\n"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestVpc/aws_exa","Output":"=== CONT  TestVpc/aws_exa\n"}`,
		// Another parallel subtest resumes while the first is still running
		// and only then spawns subtests of its own: announced on the resume,
		// so it gets a finish line too, uncounted.
		`{"Action":"run",` + pkg + `,"Test":"TestNested"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestNested","Output":"=== RUN   TestNested\n"}`,
		`{"Action":"run",` + pkg + `,"Test":"TestNested/aws_exa"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestNested/aws_exa","Output":"=== RUN   TestNested/aws_exa\n"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestNested/aws_exa","Output":"=== PAUSE TestNested/aws_exa\n"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestNested/aws_exa","Output":"=== CONT  TestNested/aws_exa\n"}`,
		`{"Action":"run",` + pkg + `,"Test":"TestNested/aws_exa/GP3"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestNested/aws_exa/GP3","Output":"=== RUN   TestNested/aws_exa/GP3\n"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestNested/aws_exa/GP3","Output":"--- PASS: TestNested/aws_exa/GP3 (0.30s)\n"}`,
		`{"Action":"pass",` + pkg + `,"Test":"TestNested/aws_exa/GP3","Elapsed":0.3}`,
		`{"Action":"output",` + pkg + `,"Test":"TestNested/aws_exa","Output":"--- PASS: TestNested/aws_exa (0.30s)\n"}`,
		`{"Action":"pass",` + pkg + `,"Test":"TestNested/aws_exa","Elapsed":0.3}`,
		`{"Action":"output",` + pkg + `,"Test":"TestNested","Output":"--- PASS: TestNested (0.30s)\n"}`,
		`{"Action":"pass",` + pkg + `,"Test":"TestNested","Elapsed":0.3}`,
		`{"Action":"output",` + pkg + `,"Test":"TestVpc/aws_exa","Output":"    vpc_test.go:10: checking subnets\n"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestVpc/aws_exa","Output":"--- PASS: TestVpc/aws_exa (7.70s)\n"}`,
		`{"Action":"pass",` + pkg + `,"Test":"TestVpc/aws_exa","Elapsed":7.7}`,
		`{"Action":"output",` + pkg + `,"Test":"TestVpc","Output":"--- PASS: TestVpc (7.70s)\n"}`,
		`{"Action":"pass",` + pkg + `,"Test":"TestVpc","Elapsed":7.7}`,
		// A test without any output of its own still gets both lines.
		`{"Action":"run",` + pkg + `,"Test":"TestQuiet"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestQuiet","Output":"=== RUN   TestQuiet\n"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestQuiet","Output":"--- FAIL: TestQuiet (0.10s)\n"}`,
		`{"Action":"fail",` + pkg + `,"Test":"TestQuiet","Elapsed":0.1}`,
		// A test that skips with a reason started: the reason is body output.
		`{"Action":"run",` + pkg + `,"Test":"TestSkipped"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestSkipped","Output":"=== RUN   TestSkipped\n"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestSkipped","Output":"    env.go:12: not selected\n"}`,
		`{"Action":"output",` + pkg + `,"Test":"TestSkipped","Output":"--- SKIP: TestSkipped (0.00s)\n"}`,
		`{"Action":"skip",` + pkg + `,"Test":"TestSkipped","Elapsed":0}`,
		`{"Action":"fail",` + pkg + `,"Elapsed":7.9}`,
	}
	var s summary
	out := captureStdout(t, func() {
		s = format(strings.NewReader(strings.Join(events, "\n")+"\n"), config, t.TempDir(), false)
	})
	assert.Equal(t, []string{
		"~ aws/vpc TestVpc/aws_exa",
		"~ aws/vpc TestNested/aws_exa",
		"~ aws/vpc TestNested/aws_exa/GP3",
		"✓ aws/vpc TestNested/aws_exa/GP3 (300ms)",
		"✓ aws/vpc TestNested/aws_exa (300ms)",
		"✓ aws/vpc TestVpc/aws_exa (7.7s)",
		"~ aws/vpc TestQuiet",
		"✗ aws/vpc TestQuiet (100ms)",
		"~ aws/vpc TestSkipped",
		"- aws/vpc TestSkipped skipped: not selected",
	}, strings.Split(strings.TrimRight(out, "\n"), "\n"))
	assert.Equal(t, 2, s.passed)
	assert.Equal(t, 1, s.failed)
	assert.Equal(t, 1, s.skipped)
	assert.Equal(t, []string{"aws/vpc TestQuiet"}, s.failures)
}
