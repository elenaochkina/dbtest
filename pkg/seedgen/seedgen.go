package seedgen

import "math/rand/v2"

// Seeder wraps a random number generator with a fixed starting seed.
// Using the same seed always produces the same sequence of numbers.
type Seeder struct {
	rng *rand.Rand
}

// New creates a Seeder locked to the given seed value.
// Call it with the same seed each test run to get identical data every time.
func New(seed int64) *Seeder {
	return &Seeder{
		rng: rand.New(rand.NewPCG(uint64(seed), 0)),
	}
}

// Intn returns the next non-negative random integer in [0, n).
func (s *Seeder) Intn(n int) int {
	return s.rng.IntN(n)
}

// StockCount returns the next stock value in the range [1000, 9999].
// Each call advances the sequence by one position.
func (s *Seeder) StockCount() int {
	return s.Intn(9000) + 1000
}
