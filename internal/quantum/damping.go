package quantum

import "math"

func ReputationFactor(rep, gamma, t float64) float64 {
	if rep >= 0 {
		return 1.0
	}
	return math.Exp(-2.0 * gamma * math.Abs(rep) * t)
}
