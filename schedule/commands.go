package schedule

import (
	"strings"
	"time"

	"github.com/atobeach/reef/clock"
	"github.com/atobeach/reef/console"
)

// Commands are schedule:run, schedule:work and schedule:list. runner builds
// a Runner over the app's kernel and locker when one of them runs.
func Commands(s *Schedule, runner func(c *console.Context) *Runner) []console.Command {
	return []console.Command{
		console.Func("schedule:run", "Run the scheduled commands", func(c *console.Context) error {
			return runner(c).RunOnce(c)
		}),
		console.Func("schedule:work", "Start the schedule worker", func(c *console.Context) error {
			return runner(c).Work(c)
		}),
		console.Func("schedule:list", "List all scheduled tasks", func(c *console.Context) error {
			now := clock.Now(c)

			width := 0
			for _, event := range s.Events() {
				width = max(width, len(event.Summary()))
			}

			for _, event := range s.Events() {
				flags := []string{}
				if event.IsOnOneServer() {
					flags = append(flags, "one server")
				}

				if event.PreventsOverlaps() {
					flags = append(flags, "no overlap")
				}

				if event.RunsInBackground() {
					flags = append(flags, "background")
				}

				c.Linef("  %-14s %-*s  Next Due: %s  [%s]",
					event.Expression(), width, event.Summary(),
					event.NextDue(now).Format(time.DateTime), strings.Join(flags, ", "))
			}

			return nil
		}),
	}
}
