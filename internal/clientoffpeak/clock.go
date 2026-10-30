package clientoffpeak

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/phamhung075/4genthub-client/internal/clientcmd"
)

// OwnerTZEnv names the owner's time zone (an IANA name such as Europe/Paris). It is read from the process
// environment, which the client fills from its one env file at start; unset means the machine's own zone.
const OwnerTZEnv = "OWNER_TZ"

// clockDriftLimit is the drift past which the machine clock is reported as wrong: WSL clocks fall behind
// after the host sleeps, and a seat that stamps its notes from such a clock writes times that never were.
const clockDriftLimit = 5 * time.Second

// clockProbeTimeout bounds each internet time probe, so an offline machine answers "unverified" quickly.
const clockProbeTimeout = 5 * time.Second

// clockSources are read in order; the first that answers with a Date header is the internet time.
var clockSources = []string{"https://www.google.com", "https://cloudflare.com"}

// internetTime is the probe, a var so a test answers it without a network. It returns the Date header of the
// first source that has one, and the source's URL; ok is false when none answered.
var internetTime = func(ctx context.Context) (at time.Time, source string, ok bool) {
	client := &http.Client{Timeout: clockProbeTimeout}
	for _, url := range clockSources {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if parsed, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
			return parsed, url, true
		}
	}
	return time.Time{}, "", false
}

// clockNow is the machine clock, a var so a test pins it.
var clockNow = time.Now

const clockUsage = `usage: 4genteam clock [--short|--json]

The one clock for the owner, the director and every seat: never write a time you did not read here.
Prints the owner's local time and UTC from the machine clock, checks the machine clock against internet
time (the Date header of google.com, then cloudflare.com, 5 s each; WSL clocks drift after the host
sleeps), and names the DeepSeek price window and its next change (4genteam offpeak's schedule:
PEAK 01:00-04:00 and 06:00-10:00 UTC Mon-Fri, off-peak otherwise and on DEEPSEEK_HOLIDAYS days).

  --short  one stamp line in the owner's zone, e.g. "2026-10-28 06:25 CET"; no network
  --json   the full report as one JSON object, for scripts

The owner's zone is OWNER_TZ (an IANA name, e.g. Europe/Paris; set it in the client env file), else the
machine's own zone. Exit 0; 1 when the machine clock is off by more than 5 s.
`

type clockCommand struct{}

func (clockCommand) Name() string { return "clock" }
func (clockCommand) Summary() string {
	return "the owner's time and UTC, checked against internet time, with the DeepSeek price window"
}

// ClockReport is what `clock --json` prints.
type ClockReport struct {
	Zone        string `json:"zone"`
	Local       string `json:"local"`
	UTC         string `json:"utc"`
	Short       string `json:"short"`
	Check       string `json:"check"` // ok, drift, unverified
	DriftSecs   *int64 `json:"drift_seconds,omitempty"`
	Source      string `json:"source,omitempty"`
	Price       string `json:"price"` // PEAK or OFF-PEAK
	NextPrice   string `json:"next_price"`
	NextChange  string `json:"next_change_utc"`
	NextLocal   string `json:"next_change_local"`
	MinutesLeft int64  `json:"minutes_to_change"`
}

func (clockCommand) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if clientcmd.HelpRequested(args) {
		_, _ = io.WriteString(stdout, clockUsage)
		return clientcmd.ExitOK
	}
	flags := flag.NewFlagSet("clock", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	short := flags.Bool("short", false, "")
	asJSON := flags.Bool("json", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		fmt.Fprintf(stderr, "4genteam clock: %s", clockUsage)
		return clientcmd.ExitUsage
	}
	zone, err := ownerZone(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "4genteam clock: %v\n", err)
		return clientcmd.ExitUsage
	}
	now := clockNow()
	if *short {
		fmt.Fprintln(stdout, shortStamp(now, zone))
		return clientcmd.ExitOK
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*clockProbeTimeout+time.Second)
	defer cancel()
	net, source, reached := internetTime(probeCtx)
	report := buildClockReport(now, zone, net, source, reached, LoadHolidays(os.Getenv(HolidaysEnv)))
	if *asJSON {
		body, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(stdout, string(body))
	} else {
		fmt.Fprint(stdout, renderClock(report))
	}
	if report.Check == "drift" {
		return 1
	}
	return clientcmd.ExitOK
}

// ownerZone is OWNER_TZ when set, else the machine's zone. A name the zone database does not know is an error
// naming the variable: a report in a zone nobody asked for is a time that reads right and is not.
func ownerZone(getenv func(string) string) (*time.Location, error) {
	name := strings.TrimSpace(getenv(OwnerTZEnv))
	if name == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%s=%q is not a time zone name (e.g. Europe/Paris): %v", OwnerTZEnv, name, err)
	}
	return loc, nil
}

// ShortNow is `4genteam clock --short` for code: the machine clock in the owner's zone, the stamp every line
// an agent writes carries. A zone the database does not know falls back to the machine's own zone.
func ShortNow(getenv func(string) string) string {
	zone, err := ownerZone(getenv)
	if err != nil {
		zone = time.Local
	}
	return shortStamp(clockNow(), zone)
}

// shortStamp is the one-line stamp seats put on what they write.
func shortStamp(now time.Time, zone *time.Location) string {
	return now.In(zone).Format("2006-01-02 15:04 MST")
}

// NextChange is the first instant after now at which the price window flips, read from At hour by hour: every
// boundary the schedule has (the peak blocks, a weekend, a holiday's midnight) falls on a whole UTC hour. A week
// and a day of hours covers a weekend next to a holiday week; past that it answers the zero time.
func NextChange(now time.Time, holidays *Holidays) time.Time {
	state := At(now, holidays).Peak
	t := now.UTC().Truncate(time.Hour).Add(time.Hour)
	for i := 0; i < 24*8; i++ {
		if At(t, holidays).Peak != state {
			return t
		}
		t = t.Add(time.Hour)
	}
	return time.Time{}
}

func buildClockReport(now time.Time, zone *time.Location, net time.Time, source string, reached bool, holidays *Holidays) ClockReport {
	verdict := At(now, holidays)
	r := ClockReport{
		Zone:  zone.String(),
		Local: now.In(zone).Format("2006-01-02 15:04:05 MST (Mon)"),
		UTC:   now.UTC().Format("2006-01-02 15:04:05"),
		Short: shortStamp(now, zone),
		Check: "unverified",
		Price: verdict.State(),
	}
	if reached {
		drift := int64(now.Sub(net).Round(time.Second) / time.Second)
		r.DriftSecs, r.Source = &drift, source
		r.Check = "ok"
		if drift > int64(clockDriftLimit/time.Second) || -drift > int64(clockDriftLimit/time.Second) {
			r.Check = "drift"
		}
	}
	if next := NextChange(now, holidays); !next.IsZero() {
		r.NextPrice = At(next, holidays).State()
		r.NextChange = next.Format("2006-01-02T15:04") + "Z"
		r.NextLocal = next.In(zone).Format("Mon 15:04 MST")
		r.MinutesLeft = int64(next.Sub(now) / time.Minute)
	}
	return r
}

func renderClock(r ClockReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "local  %s  [%s]\n", r.Local, r.Zone)
	fmt.Fprintf(&b, "UTC    %s\n", r.UTC)
	switch r.Check {
	case "ok":
		fmt.Fprintf(&b, "check  machine clock matches internet time (drift %ds, %s)\n", *r.DriftSecs, r.Source)
	case "drift":
		fmt.Fprintf(&b, "check  WARNING machine clock is off by %ds against internet time (%s); use internet time\n", *r.DriftSecs, r.Source)
	default:
		b.WriteString("check  internet time unreachable; machine clock unverified\n")
	}
	if r.NextChange != "" {
		fmt.Fprintf(&b, "price  DeepSeek %s now; %s from %s (%s, in %d min)\n", r.Price, r.NextPrice, r.NextLocal, r.NextChange, r.MinutesLeft)
	} else {
		fmt.Fprintf(&b, "price  DeepSeek %s now\n", r.Price)
	}
	return b.String()
}
