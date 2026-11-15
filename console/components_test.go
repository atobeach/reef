package console

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestComponentsRenderDottedLines(t *testing.T) {
	t.Setenv("COLUMNS", "40")

	var buf bytes.Buffer
	out := NewOutput(&buf)

	out.TwoColumnDetail("SeedCountries", "SKIPPED")
	if got := buf.String(); got != "  SeedCountries "+strings.Repeat(".", 14)+" SKIPPED  \n" {
		t.Fatalf("twoColumnDetail = %q", got)
	}

	buf.Reset()
	if err := out.Task("SeedCities", func() error { return nil }); err != nil {
		t.Fatal(err)
	}

	if got := buf.String(); !strings.HasPrefix(got, "  SeedCities ") || !strings.HasSuffix(got, "ms DONE\n") {
		t.Fatalf("task = %q", got)
	}

	buf.Reset()
	boom := errors.New("boom")
	if err := out.Task("SeedPlaces", func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("task error = %v", err)
	}

	if !strings.HasSuffix(buf.String(), " FAIL\n") {
		t.Fatalf("failed task = %q", buf.String())
	}
}

func TestWriteHasNoNewlineAndProgressBarsDrawStartAndFinish(t *testing.T) {
	var buf bytes.Buffer
	out := NewOutput(&buf)

	out.Write(".")
	out.Write(".")

	bar := out.CreateProgressBar(3)
	bar.Start()
	bar.Advance()
	bar.Advance()
	bar.Advance()
	bar.Finish()

	want := ".. 0/3 [" + strings.Repeat("\u2591", 28) + "]   0%\n 3/3 [" + strings.Repeat("\u2593", 28) + "] 100%"
	if got := buf.String(); got != want {
		t.Fatalf("output = %q", got)
	}

	if runTimeForHumans(1500*time.Millisecond) != "1s" || runTimeForHumans(65*time.Second) != "1m 5s" || runTimeForHumans(2*time.Millisecond) != "2.00ms" {
		t.Error("runTimeForHumans")
	}
}
