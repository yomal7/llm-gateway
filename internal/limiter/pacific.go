package limiter

import "time"

var pacific = loadPacific()

func loadPacific() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return time.FixedZone("PT-fallback", -8*60*60)
	}
	return loc
}

func nextPacificMidnight(from time.Time) time.Time {
	inPT := from.In(pacific)
	y, m, d := inPT.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, pacific)
	if !midnight.After(inPT) {
		midnight = midnight.AddDate(0, 0, 1)
	}
	return midnight
}