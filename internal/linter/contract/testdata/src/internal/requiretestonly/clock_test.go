package requiretestonly

import "time"

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }
