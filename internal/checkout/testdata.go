package checkout

import (
	"math/rand"
	"strconv"
)

func randomUser() User {
	tier := "free"
	if rand.Float64() < 0.3 {
		tier = "premium"
	}

	return User{
		ID:     "user_" + strconv.FormatInt(rand.Int63(), 36),
		Tier:   tier,
		Region: "us-east",
		VIP:    tier == "premium" && rand.Float64() < 0.05,
	}
}
