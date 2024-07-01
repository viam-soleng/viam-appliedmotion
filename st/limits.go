package st

import (
	"go.viam.com/rdk/logging"
)

type limits struct {
	name string
	min  float64
	max  float64
}

func newLimits(name string, min, max float64) limits {
	return limits{
		name: name,
		min:  min,
		max:  max,
	}
}

// Bound returns the value, unless it is above the max or below the min, in which case it logs a
// warning and returns one of those instead. Any floats that are 0 are ignored (so, a min of 0 is
// skipped, a max of 0 is skipped, and a value of 0 is returned immediately).
func (l *limits) Bound(value float64, logger logging.Logger) float64 {
	if value == 0 {
		// It's the default value that isn't even going to be used. Just return it as-is.
		return value
	}

	if l.min != 0 && value < l.min {
		logger.Warnf("%s is too low: asked for %f but setting to minimum %f", l.name, value, l.min)
		return l.min
	}

	if l.max != 0 && value > l.max {
		logger.Warnf("%s is too high: asked for %f but setting to maximum %f", l.name, value, l.max)
		return l.max
	}

	return value
}
